package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// decryptPush is an independent RFC 8291 receiver used to check encryptPush.
func decryptPush(t *testing.T, ua *ecdh.PrivateKey, authSecret, body []byte) []byte {
	t.Helper()
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	if rs != 4096 || idlen != 65 {
		t.Fatalf("header rs=%d idlen=%d", rs, idlen)
	}
	asPub, err := ecdh.P256().NewPublicKey(body[21 : 21+idlen])
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := ua.ECDH(asPub)
	info := append(append([]byte("WebPush: info\x00"), ua.PublicKey().Bytes()...), asPub.Bytes()...)
	ikm, _ := hkdf.Key(sha256.New, shared, authSecret, string(info), 32)
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, body[21+idlen:], nil)
	if err != nil {
		t.Fatal("decrypt:", err)
	}
	if plain[len(plain)-1] != 0x02 {
		t.Fatal("missing last-record delimiter")
	}
	return plain[:len(plain)-1]
}

func testSubscription(t *testing.T, endpoint string) (PushSubscription, *ecdh.PrivateKey, []byte) {
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	rand.Read(auth)
	enc := base64.RawURLEncoding
	return PushSubscription{Endpoint: endpoint, P256dh: enc.EncodeToString(ua.PublicKey().Bytes()), Auth: enc.EncodeToString(auth)}, ua, auth
}

func TestPushEncryptionRoundTrip(t *testing.T) {
	sub, ua, auth := testSubscription(t, "https://fcm.googleapis.com/fcm/send/x")
	body, err := encryptPush(sub, []byte(`{"title":"作業完了"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := decryptPush(t, ua, auth, body); string(got) != `{"title":"作業完了"}` {
		t.Fatalf("%q", got)
	}
}

func TestPushDeliveryVAPIDAndCleanup(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	var auths []string
	status := 201
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies, auths = append(bodies, b), append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Content-Encoding") != "aes128gcm" || r.Header.Get("TTL") == "" {
			w.WriteHeader(400)
			return
		}
		w.WriteHeader(status)
	}))
	defer ts.Close()
	store, err := OpenPushStore(filepath.Join(t.TempDir(), "push.json"), "mailto:owner@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	store.client = ts.Client()
	sub, ua, auth := testSubscription(t, ts.URL+"/push/abc")
	store.subs = []PushSubscription{sub}
	if err = store.Send(context.Background(), PushMessage{Title: "t", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	var m PushMessage
	if json.Unmarshal(decryptPush(t, ua, auth, bodies[0]), &m) != nil || m.Title != "t" || m.Body != "b" {
		t.Fatal(m)
	}
	// VAPID: "vapid t=JWT, k=PUBLIC" signed by the store key with aud = endpoint origin.
	parts := strings.SplitN(strings.TrimPrefix(auths[0], "vapid t="), ", k=", 2)
	if len(parts) != 2 || parts[1] != store.PublicKey() {
		t.Fatal(auths[0])
	}
	seg := strings.Split(parts[0], ".")
	claims, _ := base64.RawURLEncoding.DecodeString(seg[1])
	var c map[string]any
	json.Unmarshal(claims, &c)
	if c["aud"] != ts.URL || c["sub"] != "mailto:owner@example.invalid" {
		t.Fatal(c)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(seg[2])
	digest := sha256.Sum256([]byte(seg[0] + "." + seg[1]))
	if !ecdsa.Verify(&store.key.PublicKey, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("VAPID signature invalid")
	}
	status = 410
	store.Send(context.Background(), PushMessage{Title: "x"})
	if store.Count() != 0 {
		t.Fatal("gone subscription kept")
	}
}

func TestPushStorePersistenceAndValidation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "push.json")
	a, err := OpenPushStore(p, "mailto:x@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	sub, _, _ := testSubscription(t, "https://web.push.apple.com/QW123")
	if err = a.Subscribe(sub); err != nil {
		t.Fatal(err)
	}
	b, err := OpenPushStore(p, "mailto:x@example.invalid")
	if err != nil || b.PublicKey() != a.PublicKey() || b.Count() != 1 {
		t.Fatal("not persisted", err)
	}
	for _, bad := range []string{"http://fcm.googleapis.com/x", "https://evil.example/push", "https://fcm.googleapis.com.evil.example/x", "https://169.254.169.254/x", "https://user@fcm.googleapis.com/x", "https://fcm.googleapis.com:8443/x"} {
		s := sub
		s.Endpoint = bad
		if a.Subscribe(s) == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	s := sub
	s.P256dh = "AAAA"
	if a.Subscribe(s) == nil {
		t.Fatal("bad key accepted")
	}
}

// RFC 8291 Appendix A.
func TestPushEncryptionRFC8291Vector(t *testing.T) {
	d := func(s string) []byte {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	asPriv, err := ecdh.P256().NewPrivateKey(d("yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	uaPub, err := ecdh.P256().NewPublicKey(d("BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := encryptPushWith(uaPub, d("BTBZMqHH6r4Tts7J_aSIgg"), asPriv, d("DGv6ra1nlYgDCS1FRnbzlw"), []byte("When I grow up, I want to be a watermelon"))
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if got := base64.RawURLEncoding.EncodeToString(body); got != want {
		t.Fatalf("\ngot  %s\nwant %s", got, want)
	}
}
