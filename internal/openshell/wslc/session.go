package wslc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	computev1 "iwato/internal/openshell/computev1"
)

// mainProcessSpec is OpenShell's MainProcessConfig: no command means the
// sandbox picks a login shell, with a TTY.
func mainProcessSpec(spec *computev1.DriverSandboxSpec) []byte {
	cfg := struct {
		Version                    int      `json:"version"`
		Command                    []string `json:"command"`
		TTY                        bool     `json:"tty"`
		AwaitMainProcessAttachment bool     `json:"await_main_process_attachment"`
	}{1, []string{}, true, false}
	if len(spec.GetCommand()) > 0 {
		cfg.Command, cfg.TTY, cfg.AwaitMainProcessAttachment = spec.GetCommand(), spec.GetTty(), spec.GetAwaitMainProcessAttachment()
	}
	b, _ := json.Marshal(cfg)
	return b
}

func base64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// rotate gives a stopped workload a new session before it starts again:
// new boundary TLS, the generation and its evidence, the bootstrap's
// session fields and keys, the descriptor's session and trust anchor, and
// the supervisor's credentials (refresh_docker_boundary_authentication).
func (d *driver) rotate(ctx context.Context, c *containerInfo, encoded []byte) error {
	auth, err := decodeLaunchAuth(encoded)
	if err != nil {
		return err
	}
	id := c.label(labelID)
	st, err := d.readState(id, "boundary-bootstrap.json", "runtime-descriptor.json", "identity")
	if err != nil {
		return err
	}
	var bootstrap, descriptor map[string]any
	if err := json.Unmarshal(st["boundary-bootstrap.json"], &bootstrap); err != nil {
		return err
	}
	if err := json.Unmarshal(st["runtime-descriptor.json"], &descriptor); err != nil {
		return err
	}
	tls, err := newSandboxTLS(auth.session.SessionID)
	if err != nil {
		return err
	}
	// Each start is a new generation, to which both files and their
	// evidence are bound again.
	key := evidenceKey
	if _, ok := bootstrap[legacyEvidenceKey]; ok {
		key = legacyEvidenceKey
	}
	claims, _ := bootstrap["resource_claims"].(map[string]any)
	cid, _ := claims[claimContainerID].(string)
	if cid == "" {
		cid = c.ID
	}
	evidence, err := wslcEvidence(auth.generation(), cid, key == legacyEvidenceKey)
	if err != nil {
		return err
	}
	bootstrap[key], descriptor[key] = evidence, evidence
	bootstrap["generation"], descriptor["generation"] = auth.generation(), auth.generation()
	bootstrap["session_id"] = auth.session.SessionID
	bootstrap["session_rotation"] = auth.session.SessionRotation
	bootstrap["auth_epoch"] = auth.session.AuthEpoch
	bootstrap["gateway_id"] = auth.GatewayID
	bootstrap["verification_keys"] = auth.gatewayKeys()
	descriptor["session_id"] = auth.session.SessionID
	descriptor["tls"] = map[string]string{"server_name": tls.ServerName, "trust_anchor_pem": tls.TrustAnchorPEM}
	b, err := json.Marshal(bootstrap)
	if err != nil {
		return err
	}
	desc, err := json.Marshal(descriptor)
	if err != nil {
		return err
	}
	uid, gid, err := parseIDPair(string(st["identity"]))
	if err != nil {
		return err
	}
	if err := d.writeState(id, map[string][]byte{
		"boundary-bootstrap.json": b, "boundary-server.crt": []byte(tls.CertPEM), "boundary-server.key": []byte(tls.KeyPEM),
		"runtime-descriptor.json": desc, "supervisor-auth.json": auth.Supervisor,
	}); err != nil {
		return err
	}
	archive, err := buildTar([]tarEntry{
		{name: "sandbox", dir: true, mode: 0o711, uid: uid, gid: gid},
		{name: "sandbox/bootstrap.json", mode: 0o600, uid: uid, gid: gid, data: b},
		{name: "sandbox/server.crt", mode: 0o600, uid: uid, gid: gid, data: []byte(tls.CertPEM)},
		{name: "sandbox/server.key", mode: 0o600, uid: uid, gid: gid, data: []byte(tls.KeyPEM)},
	})
	if err != nil {
		return err
	}
	if err := d.w.copyIn(ctx, c.ID, channelMount, archive); err != nil {
		return err
	}
	// A sandbox made before the image carried the daemon gets the current
	// one; the workload is stopped here.
	// (A state without the main process's spec gets nothing.)
	main, _ := d.readState(id, "main-process.json")
	if entries := d.legacyTermEntries(main["main-process.json"]); entries != nil {
		archive, err := buildTar(entries)
		if err != nil {
			return err
		}
		return d.w.copyIn(ctx, c.ID, "/", archive)
	}
	return nil
}

func parseIDPair(s string) (int, int, error) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), ":")
	u, err1 := strconv.Atoi(a)
	g, err2 := strconv.Atoi(b)
	if !ok || err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("bad stored identity %q", s)
	}
	return u, g, nil
}
