package webpush

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// decryptAsBrowser undoes Encrypt from the receiving side of RFC 8291.
func decryptAsBrowser(t *testing.T, ua *ecdh.PrivateKey, authSecret, body []byte) []byte {
	t.Helper()
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	if rs != 4096 || idlen != 65 {
		t.Fatalf("header: rs=%d idlen=%d", rs, idlen)
	}
	asPub := body[21 : 21+idlen]
	as, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := ua.ECDH(as)
	prkKey, _ := hkdf.Extract(sha256.New, shared, authSecret)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(ua.PublicKey().Bytes())+string(asPub), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, body[21+idlen:], nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if plain[len(plain)-1] != 2 {
		t.Fatalf("no last-record delimiter")
	}
	return plain[:len(plain)-1]
}

func newBrowser(t *testing.T, endpoint string) (Subscription, *ecdh.PrivateKey, []byte) {
	t.Helper()
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	rand.Read(auth)
	var sub Subscription
	sub.Endpoint = endpoint
	sub.Keys.P256dh = b64.EncodeToString(ua.PublicKey().Bytes())
	sub.Keys.Auth = b64.EncodeToString(auth) + "==" // padding is tolerated
	return sub, ua, auth
}

func TestEncryptRoundTrip(t *testing.T) {
	sub, ua, auth := newBrowser(t, "https://fcm.googleapis.com/fcm/send/x")
	msg := []byte(`{"title":"Below","body":"6 épisodes disponibles"}`)
	body, err := Encrypt(sub, msg)
	if err != nil {
		t.Fatal(err)
	}
	if got := decryptAsBrowser(t, ua, auth, body); string(got) != string(msg) {
		t.Fatalf("got %q", got)
	}
}

func TestKeysAndVAPID(t *testing.T) {
	k, err := NewKeys()
	if err != nil {
		t.Fatal(err)
	}
	k2, err := ParseKeys(k.Marshal())
	if err != nil || k2.PublicKey() != k.PublicKey() {
		t.Fatalf("round trip: %v", err)
	}
	h, err := k.vapid("https://updates.push.services.mozilla.com/wpush/v2/abc", "https://agenda.example.com", time.Unix(1000, 0))
	if err != nil {
		t.Fatal(err)
	}
	tok, kpub, ok := strings.Cut(strings.TrimPrefix(h, "vapid t="), ", k=")
	if !ok || kpub != k.PublicKey() {
		t.Fatalf("header %q", h)
	}
	parts := strings.Split(tok, ".")
	claims, _ := b64.DecodeString(parts[1])
	if !strings.Contains(string(claims), `"aud":"https://updates.push.services.mozilla.com"`) {
		t.Fatalf("claims %s", claims)
	}
	sig, _ := b64.DecodeString(parts[2])
	if !verifyES256(&k.Private.PublicKey, parts[0]+"."+parts[1], sig) {
		t.Fatal("bad signature")
	}
}

func TestAllowedEndpoint(t *testing.T) {
	for u, want := range map[string]bool{
		"https://fcm.googleapis.com/fcm/send/abc":           true,
		"https://web.push.apple.com/QGuQ":                   true,
		"https://updates.push.services.mozilla.com/wpush/x": true,
		"http://fcm.googleapis.com/x":                       false,
		"https://fcm.googleapis.com:8443/x":                 false,
		"https://evil.com/fcm.googleapis.com":               false,
		"https://push.apple.com.evil.com/x":                 false,
		"https://127.0.0.1/x":                               false,
	} {
		if AllowedEndpoint(u) != want {
			t.Errorf("%s: want %v", u, want)
		}
	}
}

func TestSendGone(t *testing.T) {
	var got []byte
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		if r.Header.Get("Content-Encoding") != "aes128gcm" || !strings.HasPrefix(r.Header.Get("Authorization"), "vapid t=") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusGone)
	}))
	defer ts.Close()
	k, _ := NewKeys()
	s := &Sender{Keys: k, Subject: "https://a.example", HTTP: ts.Client()}
	sub, _, _ := newBrowser(t, ts.URL)
	if err := s.send(context.Background(), sub, []byte("x")); !errors.Is(err, ErrGone) {
		t.Fatalf("want ErrGone, got %v", err)
	}
	if len(got) < 21+65+17 {
		t.Fatalf("body %d bytes", len(got))
	}
}
