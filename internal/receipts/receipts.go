// Package receipts implements Proof-of-Inference: every gateway call gets an
// Ed25519-signed receipt (compact JWS) attesting who called, digests of the
// exact request/response bytes, the model and provider that served it, the
// guardrail policy versions in force and what they did, and the metered usage.
// Receipts carry no prompt content and can be verified offline with the
// published public key. Batches are anchored as Merkle roots in the
// hash-chained audit log so deletions or edits are detectable.
package receipts

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type Principal struct {
	Type string `json:"type"` // key | user
	ID   string `json:"id"`
}

type Route struct {
	Model         string `json:"model"`
	ProviderID    string `json:"provider_id,omitempty"`
	ProviderKind  string `json:"provider_kind,omitempty"`
	UpstreamModel string `json:"upstream_model,omitempty"`
	Attempts      int    `json:"attempts"`
	Cache         string `json:"cache,omitempty"` // "hit" when served from the response cache
}

type PolicyRef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Scope  string `json:"scope"`
	Mode   string `json:"mode"`
	Digest string `json:"digest"`
}

type Action struct {
	Policy string `json:"policy"`
	Rule   string `json:"rule"`
	Action string `json:"action"`
	Phase  string `json:"phase"`
	Count  int    `json:"count,omitempty"`
}

type Usage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
}

type Receipt struct {
	Version    int         `json:"v"`
	ID         string      `json:"rid"`
	Issuer     string      `json:"iss"`
	IssuedAt   int64       `json:"iat"`
	OrgID      string      `json:"org"`
	TeamID     string      `json:"team,omitempty"`
	ProjectID  string      `json:"project,omitempty"`
	Principal  Principal   `json:"sub"`
	AgentID    string      `json:"agent,omitempty"`
	Endpoint   string      `json:"endpoint"`
	Requested  string      `json:"model_requested"`
	Route      Route       `json:"route"`
	ReqSHA256  string      `json:"req_sha256"`
	RespSHA256 string      `json:"resp_sha256"`
	Stream     bool        `json:"stream"`
	Status     int         `json:"status"`
	Policies   []PolicyRef `json:"policies"`
	Actions    []Action    `json:"actions"`
	Usage      Usage       `json:"usage"`
	CostUSD    float64     `json:"cost_usd"`
}

type Signer struct {
	priv ed25519.PrivateKey
	Pub  ed25519.PublicKey
	KID  string
}

// NewSigner derives a stable signing key from the master key (or uses an explicit seed).
func NewSigner(master []byte, seed []byte) (*Signer, error) {
	if len(seed) == 0 {
		s, err := hkdf.Key(sha256.New, master, nil, "theophylax/receipts/ed25519/v1", ed25519.SeedSize)
		if err != nil {
			return nil, err
		}
		seed = s
	}
	if len(seed) != ed25519.SeedSize {
		return nil, errors.New("receipt signing seed must be 32 bytes")
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	h := sha256.Sum256(pub)
	return &Signer{priv: priv, Pub: pub, KID: "tpx-" + hex.EncodeToString(h[:6])}, nil
}

var b64 = base64.RawURLEncoding

// Sign returns the compact JWS and the SHA-256 of the JWS (the Merkle leaf).
func (s *Signer) Sign(r *Receipt) (string, []byte) {
	hdr, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": s.KID, "typ": "tpx-receipt+jwt"})
	pl, _ := json.Marshal(r)
	signing := b64.EncodeToString(hdr) + "." + b64.EncodeToString(pl)
	sig := ed25519.Sign(s.priv, []byte(signing))
	jws := signing + "." + b64.EncodeToString(sig)
	h := sha256.Sum256([]byte(jws))
	return jws, h[:]
}

// JWK is the public key in RFC 8037 form.
func (s *Signer) JWK() map[string]string {
	return map[string]string{"kty": "OKP", "crv": "Ed25519", "x": b64.EncodeToString(s.Pub), "kid": s.KID, "use": "sig", "alg": "EdDSA"}
}

// Verify checks a receipt JWS against a set of public keys (by kid).
func Verify(jws string, keys map[string]ed25519.PublicKey) (*Receipt, error) {
	parts := strings.Split(jws, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed receipt")
	}
	hb, err := b64.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	var hdr struct{ Alg, Kid, Typ string }
	if err := json.Unmarshal(hb, &hdr); err != nil {
		return nil, err
	}
	if hdr.Alg != "EdDSA" {
		return nil, fmt.Errorf("unexpected alg %q", hdr.Alg)
	}
	pub, ok := keys[hdr.Kid]
	if !ok {
		return nil, fmt.Errorf("unknown signing key %q", hdr.Kid)
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		return nil, errors.New("signature verification failed")
	}
	pl, err := b64.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var r Receipt
	if err := json.Unmarshal(pl, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// ParseJWKS reads {"keys":[...]} into a kid → key map.
func ParseJWKS(b []byte) (map[string]ed25519.PublicKey, error) {
	var set struct {
		Keys []struct{ Kty, Crv, X, Kid string } `json:"keys"`
	}
	if err := json.Unmarshal(b, &set); err != nil {
		return nil, err
	}
	out := map[string]ed25519.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "OKP" || k.Crv != "Ed25519" {
			continue
		}
		x, err := b64.DecodeString(k.X)
		if err == nil && len(x) == ed25519.PublicKeySize {
			out[k.Kid] = ed25519.PublicKey(x)
		}
	}
	return out, nil
}

func SHA256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// MerkleRoot computes a binary Merkle root (RFC 6962 style domain separation).
func MerkleRoot(leaves [][]byte) []byte {
	if len(leaves) == 0 {
		h := sha256.Sum256(nil)
		return h[:]
	}
	level := make([][]byte, len(leaves))
	for i, l := range leaves {
		h := sha256.Sum256(append([]byte{0}, l...))
		level[i] = h[:]
	}
	for len(level) > 1 {
		var next [][]byte
		for i := 0; i < len(level); i += 2 {
			if i+1 == len(level) {
				next = append(next, level[i])
				continue
			}
			buf := append(append([]byte{1}, level[i]...), level[i+1]...)
			h := sha256.Sum256(buf)
			next = append(next, h[:])
		}
		level = next
	}
	return level[0]
}

// Decode parses a receipt without verifying its signature (for display only).
func Decode(jws string) (*Receipt, error) {
	parts := strings.Split(jws, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed receipt")
	}
	pl, err := b64.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var r Receipt
	return &r, json.Unmarshal(pl, &r)
}
