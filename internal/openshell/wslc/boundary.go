package wslc

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"
	"unicode/utf8"
)

// Paths shared by the workload and the supervisor through the channel
// volume, and the supervisor's own volume (as OpenShell's Docker driver).
const (
	channelMount        = "/.openshell/channel"
	bootstrapPath       = "/.openshell/channel/sandbox/bootstrap.json"
	boundarySocketPath  = "/.openshell/channel/sandbox/control.sock"
	boundaryCertPath    = "/.openshell/channel/sandbox/server.crt"
	boundaryKeyPath     = "/.openshell/channel/sandbox/server.key"
	sandboxBinaryPath   = "/.openshell/runtime/openshell-sandbox"
	supervisorMount     = "/.openshell/supervisor"
	supervisorBinary    = "/openshell-supervisor"
	supervisorHealth    = "/run/openshell/health.sock"
	caMaterialTmpfs     = "/run/openshell-supervisor-ca/material"
	defaultWorkspace    = "/sandbox"
	supervisorUID       = 65534
	isolationBackend    = "openshell-sandbox"
	claimContainerID    = "wslc.container_id"
	claimImageIdentity  = "wslc.image_identity"
	claimGPU            = "openshell.gpu"
	networkCapabilities = "policy-dns-transparent-tcp"
)

// launchAuth is the gateway's SandboxLaunchAuthentication. The supervisor
// part is passed through verbatim; the workload never sees its tokens.
type launchAuth struct {
	Supervisor       json.RawMessage   `json:"supervisor"`
	GatewayID        string            `json:"gateway_id"`
	VerificationKeys []verificationKey `json:"verification_keys"`
	session          supervisorSession
}

type verificationKey struct {
	KeyID        string `json:"key_id"`
	PublicKeyPEM []int  `json:"public_key_pem"` // serde Vec<u8>: a JSON array
}

type supervisorSession struct {
	SessionID         string          `json:"session_id"`
	RuntimeGeneration json.RawMessage `json:"runtime_generation"`
	SessionRotation   json.RawMessage `json:"session_rotation"`
	AuthEpoch         json.RawMessage `json:"auth_epoch"`
}

func decodeLaunchAuth(b []byte) (*launchAuth, error) {
	if len(b) == 0 {
		return nil, errors.New("launch authentication is required (configure gateway_jwt on the gateway)")
	}
	var a launchAuth
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, fmt.Errorf("decode launch authentication: %w", err)
	}
	if err := json.Unmarshal(a.Supervisor, &a.session); err != nil {
		return nil, fmt.Errorf("decode supervisor session: %w", err)
	}
	if a.session.SessionID == "" || len(a.session.RuntimeGeneration) == 0 || len(a.session.SessionRotation) == 0 || len(a.session.AuthEpoch) == 0 {
		return nil, errors.New("launch authentication lacks session fields")
	}
	if a.GatewayID == "" || len(a.VerificationKeys) == 0 {
		return nil, errors.New("launch authentication lacks gateway_id or verification keys")
	}
	seen := map[string]bool{}
	for _, k := range a.VerificationKeys {
		if k.KeyID == "" || len(k.PublicKeyPEM) == 0 || seen[k.KeyID] {
			return nil, errors.New("launch authentication has an invalid verification key")
		}
		seen[k.KeyID] = true
		// public_key_pem is a byte array that must hold UTF-8 PEM, as the
		// Docker driver requires; checked here, not mangled into U+FFFD.
		pemBytes := make([]byte, len(k.PublicKeyPEM))
		for i, v := range k.PublicKeyPEM {
			if v < 0 || v > 255 {
				return nil, fmt.Errorf("verification key %s is not a byte array", k.KeyID)
			}
			pemBytes[i] = byte(v)
		}
		if !utf8.Valid(pemBytes) {
			return nil, fmt.Errorf("verification key %s is not UTF-8 PEM", k.KeyID)
		}
	}
	return &a, nil
}

// generation is the runtime generation as a string (its Display form).
func (a *launchAuth) generation() string {
	var s string
	if json.Unmarshal(a.session.RuntimeGeneration, &s) == nil {
		return s
	}
	return string(a.session.RuntimeGeneration)
}

func (a *launchAuth) gatewayKeys() []map[string]string {
	keys := make([]map[string]string, 0, len(a.VerificationKeys))
	for _, k := range a.VerificationKeys {
		pemBytes := make([]byte, len(k.PublicKeyPEM))
		for i, v := range k.PublicKeyPEM {
			pemBytes[i] = byte(v)
		}
		keys = append(keys, map[string]string{"key_id": k.KeyID, "public_key_pem": string(pemBytes)})
	}
	return keys
}

// identity is a ResolvedWorkloadIdentity.
type identity struct {
	UID               uint32   `json:"uid"`
	GID               uint32   `json:"gid"`
	SupplementaryGIDs []uint32 `json:"supplementary_gids"`
	Source            string   `json:"source"`
	ResourceDigest    string   `json:"resource_digest"`
}

type outerFence struct {
	Generation     string   `json:"generation"`
	Established    []string `json:"established"`
	EvidenceDigest string   `json:"evidence_digest"`
}

// wslcOuterFence projects the workload's `--network none` (checked by
// inspect) into the four guarantees the supervisor requires, binding the
// evidence to the generation like OuterFenceGuarantees::from_enforcement_evidence.
func wslcOuterFence(generation, containerID string) (outerFence, error) {
	evidence, err := json.Marshal(struct {
		ContainerID        string   `json:"container_id"`
		NetworkMode        string   `json:"network_mode"`
		UnexpectedNetworks []string `json:"unexpected_networks"`
	}{containerID, "none", []string{}})
	if err != nil || generation == "" || containerID == "" {
		return outerFence{}, errors.New("outer fence evidence is incomplete")
	}
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(generation)))
	h := sha256.New()
	h.Write(n[:])
	h.Write([]byte(generation))
	h.Write(evidence)
	return outerFence{
		Generation:     generation,
		Established:    []string{"default_deny_egress", "no_unmanaged_egress_path", "revocation_verified", "controller_loss_fails_closed"},
		EvidenceDigest: hex.EncodeToString(h.Sum(nil)),
	}, nil
}

// sandboxTLS is the per-session server identity of the Sandbox Protocol:
// a throwaway Ed25519 CA (its key is discarded) signs one leaf for
// sandbox.<session>.openshell.internal (generate_sandbox_tls_material).
type sandboxTLS struct {
	ServerName, TrustAnchorPEM, CertPEM, KeyPEM string
}

func newSandboxTLS(sessionID string) (sandboxTLS, error) {
	name := "sandbox." + sessionID + ".openshell.internal"
	notBefore := time.Date(1975, 1, 1, 0, 0, 0, 0, time.UTC)
	notAfter := time.Date(4096, 1, 1, 0, 0, 0, 0, time.UTC)
	caPub, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return sandboxTLS{}, err
	}
	caTmpl := &x509.Certificate{
		SerialNumber: randomSerial(), Subject: pkix.Name{CommonName: "OpenShell sandbox session CA"},
		NotBefore: notBefore, NotAfter: notAfter, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, caPub, caKey)
	if err != nil {
		return sandboxTLS{}, err
	}
	ca, _ := x509.ParseCertificate(caDER)
	leafPub, leafKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return sandboxTLS{}, err
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: randomSerial(), Subject: pkix.Name{CommonName: "OpenShell sandbox runtime"},
		NotBefore: notBefore, NotAfter: notAfter, DNSNames: []string{name},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, leafPub, caKey)
	if err != nil {
		return sandboxTLS{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return sandboxTLS{}, err
	}
	return sandboxTLS{
		ServerName:     name,
		TrustAnchorPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})),
		CertPEM:        string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})),
		KeyPEM:         string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
	}, nil
}

func randomSerial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	return n
}

// boundaryFiles are the generated BoundaryConfig (for openshell-sandbox)
// and SandboxRuntimeDescriptor (for openshell-supervisor) of one session.
type boundaryFiles struct {
	Bootstrap, Descriptor, SupervisorAuth []byte
	TLS                                   sandboxTLS
}

func buildBoundary(sandboxID string, auth *launchAuth, containerID, imageID string, id identity, childEnv map[string]string, gpu bool) (boundaryFiles, error) {
	tls, err := newSandboxTLS(auth.session.SessionID)
	if err != nil {
		return boundaryFiles{}, err
	}
	gen := auth.generation()
	fence, err := wslcOuterFence(gen, containerID)
	if err != nil {
		return boundaryFiles{}, err
	}
	claims := map[string]string{claimContainerID: containerID, claimImageIdentity: imageID}
	if gpu {
		// The supervisor adds /dev/dxg and /usr/lib/wsl to Landlock for it.
		claims[claimGPU] = "true"
	}
	if childEnv == nil {
		childEnv = map[string]string{}
	}
	bootstrap, err := json.Marshal(map[string]any{
		"boundary_id":       sandboxID,
		"generation":        gen,
		"session_id":        auth.session.SessionID,
		"session_rotation":  auth.session.SessionRotation,
		"auth_epoch":        auth.session.AuthEpoch,
		"gateway_id":        auth.GatewayID,
		"verification_keys": auth.gatewayKeys(),
		"listener": map[string]any{
			"kind":        "unix",
			"socket_path": boundarySocketPath,
			"tls":         map[string]string{"certificate_chain_path": boundaryCertPath, "private_key_path": boundaryKeyPath},
		},
		"resource_claims":      claims,
		"resource_claim_files": map[string]string{},
		"workload_identity":    id,
		"outer_fence":          fence,
		"child_env":            childEnv,
	})
	if err != nil {
		return boundaryFiles{}, err
	}
	descriptor, err := json.Marshal(map[string]any{
		"boundary_id":       sandboxID,
		"generation":        gen,
		"session_id":        auth.session.SessionID,
		"workload_identity": id,
		"transport":         map[string]string{"kind": "unix", "socket_path": boundarySocketPath},
		"tls":               map[string]string{"server_name": tls.ServerName, "trust_anchor_pem": tls.TrustAnchorPEM},
		"host_gateway_ip":   nil,
		"resource_claims":   claims,
		"outer_fence":       fence,
	})
	if err != nil {
		return boundaryFiles{}, err
	}
	return boundaryFiles{Bootstrap: bootstrap, Descriptor: descriptor, SupervisorAuth: auth.Supervisor, TLS: tls}, nil
}
