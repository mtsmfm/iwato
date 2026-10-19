package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Web Push without third-party code: RFC 8291 (aes128gcm message encryption)
// and RFC 8292 (VAPID). Push services are the only outbound destinations and
// are restricted to the well-known browser vendors' hosts.

var pushHosts = []string{"fcm.googleapis.com", "android.googleapis.com", ".push.apple.com", "updates.push.services.mozilla.com", ".notify.windows.com"}

func allowedPushEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || len(raw) > 2048 {
		return false
	}
	h := strings.ToLower(u.Hostname())
	for _, p := range pushHosts {
		if h == p || (p[0] == '.' && strings.HasSuffix(h, p)) {
			return true
		}
	}
	return false
}

type PushSubscription struct {
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
	Created  int64  `json:"created"`
}

type pushFile struct {
	VAPIDKey      string             `json:"vapidKey"`
	Subscriptions []PushSubscription `json:"subscriptions"`
}

// PushStore persists the VAPID key and subscriptions next to the config.
type PushStore struct {
	mu      sync.Mutex
	path    string
	key     *ecdsa.PrivateKey
	subs    []PushSubscription
	subject string
	client  *http.Client
}

func OpenPushStore(path, subject string) (*PushStore, error) {
	s := &PushStore{path: path, subject: subject, client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirects forbidden") }}}
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var f pushFile
	if err == nil {
		if err = json.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("invalid %s: %w", path, err)
		}
		der, err := base64.StdEncoding.DecodeString(f.VAPIDKey)
		if err != nil {
			return nil, err
		}
		k, err := x509.ParseECPrivateKey(der)
		if err != nil || k.Curve != elliptic.P256() {
			return nil, errors.New("invalid VAPID key")
		}
		s.key, s.subs = k, f.Subscriptions
		return s, nil
	}
	if s.key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
		return nil, err
	}
	return s, s.saveLocked()
}

func (s *PushStore) saveLocked() error {
	der, err := x509.MarshalECPrivateKey(s.key)
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(pushFile{VAPIDKey: base64.StdEncoding.EncodeToString(der), Subscriptions: s.subs}, "", "  ")
	tmp := s.path + ".tmp"
	if err = os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *PushStore) PublicKey() string {
	pub, _ := s.key.PublicKey.ECDH()
	return base64.RawURLEncoding.EncodeToString(pub.Bytes())
}

func (s *PushStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

func decodeB64URL(v string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(v, "="))
}

func (s *PushStore) Subscribe(sub PushSubscription) error {
	if !allowedPushEndpoint(sub.Endpoint) {
		return errors.New("unsupported push service endpoint")
	}
	p, err := decodeB64URL(sub.P256dh)
	if err != nil || len(p) != 65 {
		return errors.New("invalid p256dh key")
	}
	if _, err = ecdh.P256().NewPublicKey(p); err != nil {
		return errors.New("invalid p256dh key")
	}
	if a, err := decodeB64URL(sub.Auth); err != nil || len(a) != 16 {
		return errors.New("invalid auth secret")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := []PushSubscription{}
	for _, old := range s.subs {
		if old.Endpoint != sub.Endpoint {
			kept = append(kept, old)
		}
	}
	sub.Created = time.Now().Unix()
	s.subs = append(kept, sub)
	if len(s.subs) > 10 {
		s.subs = s.subs[len(s.subs)-10:]
	}
	return s.saveLocked()
}

func (s *PushStore) Unsubscribe(endpoint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := []PushSubscription{}
	for _, old := range s.subs {
		if old.Endpoint != endpoint {
			kept = append(kept, old)
		}
	}
	s.subs = kept
	return s.saveLocked()
}

type PushMessage struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Tag   string `json:"tag,omitempty"`
	// Path is what a click opens ("/?sandbox=NAME&term=TERM" or "&tab=…").
	Path string `json:"path,omitempty"`
	// Local is this server's loopback origin: its port changes per launch,
	// so a PC's service worker (registered at an older port) opens this one.
	Local string `json:"local,omitempty"`
	// Badge is how many proposals wait for the owner across all sandboxes,
	// for the installed app's icon badge (set by the service worker).
	Badge *int `json:"badge,omitempty"`
	// sandbox is the sandbox the message is about ("" for one about all
	// of them); not sent.
	sandbox string
	// summary is the message in one line, for a merged message; not sent.
	summary string
	// idle marks a message about a terminal that stopped; not sent.
	idle bool
}

// Send delivers to every subscription; gone subscriptions (404/410) are removed.
func (s *PushStore) Send(ctx context.Context, m PushMessage) error {
	payload, _ := json.Marshal(m)
	s.mu.Lock()
	subs := append([]PushSubscription(nil), s.subs...)
	s.mu.Unlock()
	var errs []error
	for _, sub := range subs {
		code, err := s.deliver(ctx, sub, payload)
		if code == http.StatusNotFound || code == http.StatusGone {
			_ = s.Unsubscribe(sub.Endpoint)
			continue
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *PushStore) deliver(ctx context.Context, sub PushSubscription, payload []byte) (int, error) {
	body, err := encryptPush(sub, payload)
	if err != nil {
		return 0, err
	}
	auth, err := s.vapidHeader(sub.Endpoint)
	if err != nil {
		return 0, err
	}
	r, err := http.NewRequestWithContext(ctx, "POST", sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	r.Header.Set("Content-Encoding", "aes128gcm")
	r.Header.Set("Content-Type", "application/octet-stream")
	r.Header.Set("TTL", "86400")
	r.Header.Set("Urgency", "high")
	r.Header.Set("Authorization", auth)
	res, err := s.client.Do(r)
	if err != nil {
		return 0, errors.New("push service unreachable")
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	if res.StatusCode >= 300 {
		return res.StatusCode, fmt.Errorf("push service HTTP %d", res.StatusCode)
	}
	return res.StatusCode, nil
}

func (s *PushStore) vapidHeader(endpoint string) (string, error) {
	u, _ := url.Parse(endpoint)
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{"aud": u.Scheme + "://" + u.Host, "exp": time.Now().Add(12 * time.Hour).Unix(), "sub": s.subject})
	signing := header + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	r, sg, err := ecdsa.Sign(rand.Reader, s.key, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	sg.FillBytes(sig[32:])
	return "vapid t=" + signing + "." + enc.EncodeToString(sig) + ", k=" + s.PublicKey(), nil
}

// encryptPush implements RFC 8291 with a single aes128gcm record.
func encryptPush(sub PushSubscription, payload []byte) ([]byte, error) {
	uaRaw, err := decodeB64URL(sub.P256dh)
	if err != nil {
		return nil, err
	}
	authSecret, err := decodeB64URL(sub.Auth)
	if err != nil {
		return nil, err
	}
	uaPub, err := ecdh.P256().NewPublicKey(uaRaw)
	if err != nil {
		return nil, err
	}
	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err = rand.Read(salt); err != nil {
		return nil, err
	}
	return encryptPushWith(uaPub, authSecret, asPriv, salt, payload)
}

func encryptPushWith(uaPub *ecdh.PublicKey, authSecret []byte, asPriv *ecdh.PrivateKey, salt, payload []byte) ([]byte, error) {
	if len(payload) > 3000 {
		return nil, errors.New("push payload too large")
	}
	shared, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, err
	}
	asPub := asPriv.PublicKey().Bytes()
	keyInfo := append(append([]byte("WebPush: info\x00"), uaPub.Bytes()...), asPub...)
	ikm, err := hkdf.Key(sha256.New, shared, authSecret, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain := append(append([]byte(nil), payload...), 0x02) // last-record delimiter, no padding
	header := make([]byte, 0, 16+4+1+len(asPub))
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint32(header, 4096)
	header = append(header, byte(len(asPub)))
	header = append(header, asPub...)
	return append(header, gcm.Seal(nil, nonce, plain, nil)...), nil
}
