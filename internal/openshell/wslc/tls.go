package wslc

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Driver transport security. The gateway only dials a Unix socket and
// sends no credentials, so on Windows the relay in the gateway container
// (socat OPENSSL) presents a client certificate and this process accepts
// only that: other WSLc containers also reach Windows' loopback through
// host.wslc.internal. TLS ends here and gRPC runs plaintext over it, since
// socat negotiates no ALPN.

// tlsListen wraps a TCP listener in TLS that requires a client certificate
// signed by clientCA.
func tlsListen(addr, certFile, keyFile, clientCAFile string) (net.Listener, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("no certificates in the client CA file")
	}
	return tls.Listen("tcp", addr, &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
	})
}

// writeDriverCerts creates a CA, the driver's server certificate and the
// relay's client certificate in dir (ca.pem, server.pem/key, relay.pem/key).
func writeDriverCerts(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	now := time.Now().Add(-time.Hour)
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	ca := &x509.Certificate{
		SerialNumber: randomSerial(), Subject: pkix.Name{CommonName: "openshell-driver-wslc CA"},
		NotBefore: now, NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	ca, _ = x509.ParseCertificate(caDER)
	if err := writePEM(filepath.Join(dir, "ca.pem"), "CERTIFICATE", caDER); err != nil {
		return err
	}
	for _, c := range []struct {
		name string
		tmpl *x509.Certificate
	}{
		{"server", &x509.Certificate{
			Subject: pkix.Name{CommonName: "openshell-driver-wslc"}, DNSNames: []string{"host.wslc.internal", "localhost"},
			IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}},
		{"relay", &x509.Certificate{
			Subject: pkix.Name{CommonName: "openshell-gateway relay"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}},
	} {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return err
		}
		c.tmpl.SerialNumber, c.tmpl.NotBefore, c.tmpl.NotAfter = randomSerial(), now, now.AddDate(10, 0, 0)
		c.tmpl.KeyUsage = x509.KeyUsageDigitalSignature
		der, err := x509.CreateCertificate(rand.Reader, c.tmpl, ca, &key.PublicKey, caKey)
		if err != nil {
			return err
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return err
		}
		if err := writePEM(filepath.Join(dir, c.name+".pem"), "CERTIFICATE", der); err != nil {
			return err
		}
		if err := writePEM(filepath.Join(dir, c.name+".key"), "PRIVATE KEY", keyDER); err != nil {
			return err
		}
	}
	return nil
}

func writePEM(path, typ string, der []byte) error {
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
