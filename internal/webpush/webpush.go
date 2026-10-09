// Package webpush sends Web Push messages: payload encryption per RFC 8291
// (aes128gcm) and VAPID authentication per RFC 8292, on the standard library.
package webpush

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
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Subscription is what the browser's PushManager hands out.
type Subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// ErrGone means the push service dropped the subscription (404 or 410):
// it must be deleted.
var ErrGone = errors.New("webpush: subscription gone")

var b64 = base64.RawURLEncoding

// decode accepts base64url with or without padding.
func decode(s string) ([]byte, error) { return b64.DecodeString(strings.TrimRight(s, "=")) }

// Keys is the server's VAPID key pair.
type Keys struct {
	Private *ecdsa.PrivateKey
}

// NewKeys generates a VAPID key pair.
func NewKeys() (Keys, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	return Keys{k}, err
}

// Marshal encodes the private key (32 bytes, base64url) for storage.
func (k Keys) Marshal() string {
	b, _ := k.Private.Bytes()
	return b64.EncodeToString(b)
}

// ParseKeys decodes a key stored by Marshal.
func ParseKeys(s string) (Keys, error) {
	d, err := decode(s)
	if err != nil || len(d) != 32 {
		return Keys{}, errors.New("webpush: bad VAPID key")
	}
	k, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), d)
	if err != nil {
		return Keys{}, err
	}
	return Keys{k}, nil
}

// PublicKey is the applicationServerKey browsers subscribe with
// (uncompressed point, base64url).
func (k Keys) PublicKey() string {
	b, _ := k.Private.PublicKey.Bytes()
	return b64.EncodeToString(b)
}

// Encrypt builds an aes128gcm body for the subscription (RFC 8291).
func Encrypt(sub Subscription, payload []byte) ([]byte, error) {
	uaPub, err := decode(sub.Keys.P256dh)
	if err != nil {
		return nil, fmt.Errorf("webpush: p256dh: %w", err)
	}
	authSecret, err := decode(sub.Keys.Auth)
	if err != nil || len(authSecret) < 16 {
		return nil, errors.New("webpush: bad auth secret")
	}
	ua, err := ecdh.P256().NewPublicKey(uaPub)
	if err != nil {
		return nil, fmt.Errorf("webpush: p256dh: %w", err)
	}
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return encrypt(ua, uaPub, authSecret, as, salt, payload)
}

func encrypt(ua *ecdh.PublicKey, uaPub, authSecret []byte, as *ecdh.PrivateKey, salt, payload []byte) ([]byte, error) {
	shared, err := as.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPub := as.PublicKey().Bytes()
	prkKey, err := hkdf.Extract(sha256.New, shared, authSecret)
	if err != nil {
		return nil, err
	}
	ikm, err := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(uaPub)+string(asPub), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
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
	// One record: the payload and the last-record delimiter.
	sealed := gcm.Seal(nil, nonce, append(append([]byte{}, payload...), 2), nil)
	var out bytes.Buffer
	out.Write(salt)
	_ = binary.Write(&out, binary.BigEndian, uint32(4096))
	out.WriteByte(byte(len(asPub)))
	out.Write(asPub)
	out.Write(sealed)
	return out.Bytes(), nil
}

// vapid signs the Authorization header for an endpoint's origin (RFC 8292).
func (k Keys) vapid(endpoint, subject string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	header := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{"aud": u.Scheme + "://" + u.Host, "exp": now.Add(12 * time.Hour).Unix(), "sub": subject})
	signing := header + "." + b64.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, k.Private, sum[:])
	if err != nil {
		return "", err
	}
	sig := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	return "vapid t=" + signing + "." + b64.EncodeToString(sig) + ", k=" + k.PublicKey(), nil
}

// Sender posts messages to push services.
type Sender struct {
	Keys    Keys
	Subject string // a mailto: or https: contact for push services
	HTTP    *http.Client
}

// Send delivers payload to sub; the message is kept by the push service for
// a day if the device is offline.
func (s *Sender) Send(ctx context.Context, sub Subscription, payload []byte) error {
	if !AllowedEndpoint(sub.Endpoint) {
		return fmt.Errorf("webpush: endpoint host not allowed")
	}
	return s.send(ctx, sub, payload)
}

func (s *Sender) send(ctx context.Context, sub Subscription, payload []byte) error {
	body, err := Encrypt(sub, payload)
	if err != nil {
		return err
	}
	auth, err := s.Keys.vapid(sub.Endpoint, s.Subject, time.Now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", "86400")
	req.Header.Set("Urgency", "normal")
	req.Header.Set("Authorization", auth)
	res, err := s.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		return ErrGone
	case res.StatusCode < 200 || res.StatusCode > 299:
		return fmt.Errorf("webpush: push service answered HTTP %d", res.StatusCode)
	}
	return nil
}

// pushHosts are the push services of the major browsers. Subscriptions
// elsewhere are refused, so a client cannot make the server POST to an
// arbitrary host.
var pushHosts = []string{
	"fcm.googleapis.com", "android.googleapis.com", // Chrome, Edge on Android, Opera…
	"updates.push.services.mozilla.com", // Firefox
	".push.apple.com",                   // Safari
	".notify.windows.com",               // Edge on Windows (legacy WNS)
}

// AllowedEndpoint reports whether an endpoint is an https URL on a known
// push service.
func AllowedEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Port() != "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	for _, p := range pushHosts {
		if h == p || (strings.HasPrefix(p, ".") && strings.HasSuffix(h, p)) {
			return true
		}
	}
	return false
}

// verifyES256 checks a VAPID JWT signature (tests).
func verifyES256(pub *ecdsa.PublicKey, signing string, sig []byte) bool {
	if len(sig) != 64 {
		return false
	}
	sum := sha256.Sum256([]byte(signing))
	return ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
}
