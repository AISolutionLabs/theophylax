package migrate

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"math/big"
	"math/bits"
	"strings"
)

// LiteLLM encrypts values stored in its database (model credentials, MCP and
// guardrail settings) with a key derived as SHA-256(LITELLM_SALT_KEY, or the
// master key when no salt key is set), in one of two formats:
//
//	v2:gcm:<base64url(nonce12 ‖ ciphertext ‖ tag16)>   AES-256-GCM
//	<base64url(nonce24 ‖ tag16 ‖ ciphertext)>          NaCl secretbox (XSalsa20-Poly1305)
//
// Go's standard library has AES-GCM; secretbox is implemented below.

// DecryptLiteLLM decrypts one stored value.
func DecryptLiteLLM(value, saltKey string) (string, error) {
	key := sha256.Sum256([]byte(saltKey))
	if strings.HasPrefix(value, "v2:gcm:") {
		raw, err := b64(strings.TrimPrefix(value, "v2:gcm:"))
		if err != nil || len(raw) < 28 {
			return "", errors.New("not a LiteLLM v2:gcm value")
		}
		blk, _ := aes.NewCipher(key[:])
		g, _ := cipher.NewGCM(blk)
		out, err := g.Open(nil, raw[:12], raw[12:], nil)
		if err != nil {
			return "", errors.New("wrong salt key or corrupt value")
		}
		return string(out), nil
	}
	raw, err := b64(value)
	if err != nil || len(raw) < 24+16 {
		return "", errors.New("not a LiteLLM encrypted value")
	}
	var nonce [24]byte
	copy(nonce[:], raw[:24])
	out, ok := secretboxOpen(raw[24:], &nonce, &key)
	if !ok {
		return "", errors.New("wrong salt key or corrupt value")
	}
	return string(out), nil
}

func b64(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

// --- XSalsa20-Poly1305 (NaCl crypto_secretbox_open) ------------------------

func rotl(x uint32, n int) uint32 { return bits.RotateLeft32(x, n) }

// salsaCore runs 20 rounds of Salsa20 over in. When hsalsa is true it returns
// the HSalsa20 output words (no feed-forward), used for XSalsa20 subkeys.
func salsaCore(in *[16]uint32, hsalsa bool) [16]uint32 {
	x := *in
	for i := 0; i < 10; i++ {
		x[4] ^= rotl(x[0]+x[12], 7)
		x[8] ^= rotl(x[4]+x[0], 9)
		x[12] ^= rotl(x[8]+x[4], 13)
		x[0] ^= rotl(x[12]+x[8], 18)
		x[9] ^= rotl(x[5]+x[1], 7)
		x[13] ^= rotl(x[9]+x[5], 9)
		x[1] ^= rotl(x[13]+x[9], 13)
		x[5] ^= rotl(x[1]+x[13], 18)
		x[14] ^= rotl(x[10]+x[6], 7)
		x[2] ^= rotl(x[14]+x[10], 9)
		x[6] ^= rotl(x[2]+x[14], 13)
		x[10] ^= rotl(x[6]+x[2], 18)
		x[3] ^= rotl(x[15]+x[11], 7)
		x[7] ^= rotl(x[3]+x[15], 9)
		x[11] ^= rotl(x[7]+x[3], 13)
		x[15] ^= rotl(x[11]+x[7], 18)
		x[1] ^= rotl(x[0]+x[3], 7)
		x[2] ^= rotl(x[1]+x[0], 9)
		x[3] ^= rotl(x[2]+x[1], 13)
		x[0] ^= rotl(x[3]+x[2], 18)
		x[6] ^= rotl(x[5]+x[4], 7)
		x[7] ^= rotl(x[6]+x[5], 9)
		x[4] ^= rotl(x[7]+x[6], 13)
		x[5] ^= rotl(x[4]+x[7], 18)
		x[11] ^= rotl(x[10]+x[9], 7)
		x[8] ^= rotl(x[11]+x[10], 9)
		x[9] ^= rotl(x[8]+x[11], 13)
		x[10] ^= rotl(x[9]+x[8], 18)
		x[12] ^= rotl(x[15]+x[14], 7)
		x[13] ^= rotl(x[12]+x[15], 9)
		x[14] ^= rotl(x[13]+x[12], 13)
		x[15] ^= rotl(x[14]+x[13], 18)
	}
	if hsalsa {
		return x
	}
	for i := range x {
		x[i] += in[i]
	}
	return x
}

var sigma = [4]uint32{0x61707865, 0x3320646e, 0x79622d32, 0x6b206574} // "expand 32-byte k"

func salsaState(key *[32]byte, in16 []byte) [16]uint32 {
	var s [16]uint32
	s[0], s[5], s[10], s[15] = sigma[0], sigma[1], sigma[2], sigma[3]
	for i := 0; i < 4; i++ {
		s[1+i] = binary.LittleEndian.Uint32(key[4*i:])
		s[11+i] = binary.LittleEndian.Uint32(key[16+4*i:])
		s[6+i] = binary.LittleEndian.Uint32(in16[4*i:])
	}
	return s
}

// xsalsaStream returns n bytes of XSalsa20 keystream.
func xsalsaStream(n int, nonce *[24]byte, key *[32]byte) []byte {
	h := salsaCore(ptr(salsaState(key, nonce[:16])), true)
	var sub [32]byte
	for i, w := range []uint32{h[0], h[5], h[10], h[15], h[6], h[7], h[8], h[9]} {
		binary.LittleEndian.PutUint32(sub[4*i:], w)
	}
	out := make([]byte, 0, n+64)
	in := make([]byte, 16)
	copy(in, nonce[16:24])
	for ctr := uint64(0); len(out) < n; ctr++ {
		binary.LittleEndian.PutUint64(in[8:], ctr)
		blk := salsaCore(ptr(salsaState(&sub, in)), false)
		var b [64]byte
		for i, w := range blk {
			binary.LittleEndian.PutUint32(b[4*i:], w)
		}
		out = append(out, b[:]...)
	}
	return out[:n]
}

func ptr(s [16]uint32) *[16]uint32 { return &s }

var p1305 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 130), big.NewInt(5))

// poly1305 computes the one-time authenticator (values here are small, so
// math/big keeps this short and obviously correct).
func poly1305(msg []byte, key []byte) [16]byte {
	rb := make([]byte, 16)
	copy(rb, key[:16])
	rb[3] &= 15
	rb[7] &= 15
	rb[11] &= 15
	rb[15] &= 15
	rb[4] &= 252
	rb[8] &= 252
	rb[12] &= 252
	r := new(big.Int).SetBytes(reverse(rb))
	s := new(big.Int).SetBytes(reverse(append([]byte{}, key[16:32]...)))
	acc := new(big.Int)
	for i := 0; i < len(msg); i += 16 {
		end := min(i+16, len(msg))
		blk := append(append([]byte{}, msg[i:end]...), 1)
		acc.Add(acc, new(big.Int).SetBytes(reverse(blk)))
		acc.Mul(acc, r)
		acc.Mod(acc, p1305)
	}
	acc.Add(acc, s)
	b := acc.Bytes() // big-endian
	var tag [16]byte
	for i := 0; i < 16 && i < len(b); i++ {
		tag[i] = b[len(b)-1-i]
	}
	return tag
}

func reverse(b []byte) []byte {
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return b
}

// secretboxOpen verifies and decrypts box = tag16 ‖ ciphertext.
func secretboxOpen(box []byte, nonce *[24]byte, key *[32]byte) ([]byte, bool) {
	if len(box) < 16 {
		return nil, false
	}
	tag, ct := box[:16], box[16:]
	ks := xsalsaStream(32+len(ct), nonce, key)
	want := poly1305(ct, ks[:32])
	if subtle.ConstantTimeCompare(tag, want[:]) != 1 {
		return nil, false
	}
	out := make([]byte, len(ct))
	for i := range ct {
		out[i] = ct[i] ^ ks[32+i]
	}
	return out, true
}
