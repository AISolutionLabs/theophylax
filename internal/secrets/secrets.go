// Package secrets provides envelope-style encryption of secrets at rest (AES-256-GCM)
// and key derivation from the master key.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
)

const version byte = 1

type Box struct {
	aead       cipher.AEAD
	sessionKey []byte
}

func New(master []byte) (*Box, error) {
	encKey, err := hkdf.Key(sha256.New, master, nil, "theophylax/secrets/v1", 32)
	if err != nil {
		return nil, err
	}
	sess, err := hkdf.Key(sha256.New, master, nil, "theophylax/session/v1", 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead, sessionKey: sess}, nil
}

// SessionKey is the HMAC key for signed session cookies and OAuth state.
func (b *Box) SessionKey() []byte { return b.sessionKey }

func (b *Box) Seal(plain []byte) []byte {
	if len(plain) == 0 {
		return nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	_, _ = rand.Read(nonce)
	out := append([]byte{version}, nonce...)
	return b.aead.Seal(out, nonce, plain, []byte{version})
}

func (b *Box) Open(ct []byte) ([]byte, error) {
	if len(ct) == 0 {
		return nil, nil
	}
	ns := b.aead.NonceSize()
	if len(ct) < 1+ns || ct[0] != version {
		return nil, errors.New("secrets: invalid ciphertext")
	}
	return b.aead.Open(nil, ct[1:1+ns], ct[1+ns:], []byte{version})
}

func (b *Box) SealString(s string) []byte { return b.Seal([]byte(s)) }

func (b *Box) OpenString(ct []byte) (string, error) {
	p, err := b.Open(ct)
	return string(p), err
}
