package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// recordSize is the aes128gcm record size the host writes: the whole
// message is one record, and 4096 is what RFC 8291 §4 expects.
const recordSize = 4096

// MaxPayload is the largest plaintext one push message carries. RFC 8291
// §4: push services need only accept a 4096-octet body, which after the
// 86-octet header, the 16-octet tag and the padding delimiter leaves 3993.
const MaxPayload = 3993

// ErrPayloadTooLarge is returned for a plaintext past MaxPayload.
var ErrPayloadTooLarge = errors.New("push: payload exceeds 3993 octets")

// Encrypt seals plaintext for the browser holding keys, with a fresh
// ephemeral key and salt, into an aes128gcm body (RFC 8291, RFC 8188).
func Encrypt(keys Keys, plaintext []byte) ([]byte, error) {
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("push: ephemeral key: %w", err)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("push: salt: %w", err)
	}
	return encrypt(keys, plaintext, as, salt)
}

// encrypt is Encrypt with the ephemeral key and salt supplied, which is
// what lets a test reproduce RFC 8291 Appendix A byte for byte.
func encrypt(keys Keys, plaintext []byte, as *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(plaintext) > MaxPayload {
		return nil, ErrPayloadTooLarge
	}
	uaPublic, authSecret, err := keys.decode()
	if err != nil {
		return nil, err
	}
	ua, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, fmt.Errorf("push: p256dh: %w", err)
	}
	if len(salt) != 16 {
		return nil, fmt.Errorf("push: salt must be 16 octets, got %d", len(salt))
	}
	ecdhSecret, err := as.ECDH(ua)
	if err != nil {
		return nil, fmt.Errorf("push: ecdh: %w", err)
	}
	asPublic := as.PublicKey().Bytes()

	// RFC 8291 §3.4: fold the auth secret and both public keys into the
	// input keying material, then derive as RFC 8188 §2.2 does.
	prkKey, err := hkdf.Extract(sha256.New, ecdhSecret, authSecret)
	if err != nil {
		return nil, err
	}
	keyInfo := make([]byte, 0, 14+65+65)
	keyInfo = append(keyInfo, "WebPush: info\x00"...)
	keyInfo = append(keyInfo, uaPublic...)
	keyInfo = append(keyInfo, asPublic...)
	ikm, err := hkdf.Expand(sha256.New, prkKey, string(keyInfo), 32)
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

	// Header (RFC 8188 §2.1): salt, record size, key id length, key id —
	// the key id being the ephemeral public key (RFC 8291 §4).
	out := make([]byte, 0, 16+4+1+len(asPublic)+len(plaintext)+1+gcm.Overhead())
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, recordSize)
	out = append(out, byte(len(asPublic))) //nolint:gosec // an uncompressed P-256 point: 65 bytes
	out = append(out, asPublic...)

	// One record, so the sequence number is 0 and the nonce is used as
	// derived; 0x02 marks it the last record, with no further padding.
	record := make([]byte, 0, len(plaintext)+1)
	record = append(record, plaintext...)
	record = append(record, 0x02)
	return gcm.Seal(out, nonce, record, nil), nil
}

// Keys are a subscription's keys as PushSubscription.toJSON() gives them:
// the browser's P-256 public key and the 16-octet auth secret, base64url.
type Keys struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

func (k Keys) decode() (uaPublic, auth []byte, err error) {
	uaPublic, err = decodeB64(k.P256dh)
	if err != nil {
		return nil, nil, fmt.Errorf("push: p256dh: %w", err)
	}
	if _, err := ecdh.P256().NewPublicKey(uaPublic); err != nil {
		return nil, nil, fmt.Errorf("push: p256dh: %w", err)
	}
	auth, err = decodeB64(k.Auth)
	if err != nil {
		return nil, nil, fmt.Errorf("push: auth: %w", err)
	}
	if len(auth) != 16 {
		return nil, nil, fmt.Errorf("push: auth secret must be 16 octets, got %d", len(auth))
	}
	return uaPublic, auth, nil
}
