package receipts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"
)

// Test vectors for the receipt format (docs/receipt-spec.md). They let other
// implementations check their verifier against known-good and known-bad
// receipts, and they fail this test if the format drifts.
//
// Regenerate deliberately with: go test ./internal/receipts -run TestVectors -update

var update = flag.Bool("update", false, "rewrite testdata/vectors.json")

type vectorFile struct {
	Comment string   `json:"comment"`
	Seed    string   `json:"signing_seed_hex"`
	JWKS    any      `json:"jwks"`
	KID     string   `json:"kid"`
	Valid   []vector `json:"valid"`
	Invalid []bad    `json:"invalid"`
	Merkle  merkle   `json:"merkle"`
}

type vector struct {
	Name       string   `json:"name"`
	Request    string   `json:"request_body"`
	Response   string   `json:"response_body"`
	ReqSHA256  string   `json:"req_sha256"`
	RespSHA256 string   `json:"resp_sha256"`
	Receipt    *Receipt `json:"receipt"`
	JWS        string   `json:"jws"`
	LeafHash   string   `json:"leaf_sha256"`
}

type bad struct {
	Name   string `json:"name"`
	JWS    string `json:"jws"`
	Reason string `json:"reason"`
}

type merkle struct {
	Leaves []string `json:"leaf_sha256"`
	Root   string   `json:"root"`
}

func buildVectors(t *testing.T) vectorFile {
	seed := bytes.Repeat([]byte{0x2a}, 32)
	s, err := NewSigner(nil, seed)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name, req, resp string, stream bool, status int) vector {
		r := &Receipt{Version: 1, ID: "req_vector_" + name, Issuer: "https://gateway.example", IssuedAt: 1791000000,
			OrgID: "org_example", ProjectID: "prj_example", Principal: Principal{Type: "key", ID: "key_example"},
			Endpoint: "/v1/messages", Requested: "example-model",
			Route:     Route{Model: "example-model", ProviderID: "prv_example", ProviderKind: "anthropic", UpstreamModel: "example-upstream", Attempts: 1},
			ReqSHA256: SHA256Hex([]byte(req)), RespSHA256: SHA256Hex([]byte(resp)), Stream: stream, Status: status,
			Policies: []PolicyRef{{ID: "grd_example", Name: "Secret Shield", Scope: "org", Mode: "enforce", Digest: "sha256:" + SHA256Hex([]byte("rules"))}},
			Actions:  []Action{{Policy: "Secret Shield", Rule: "secrets", Action: "redact", Phase: "input", Count: 1}},
			Usage:    Usage{Input: 12, Output: 5}, CostUSD: 0.0001}
		jws, leaf := s.Sign(r)
		return vector{Name: name, Request: req, Response: resp, ReqSHA256: r.ReqSHA256, RespSHA256: r.RespSHA256, Receipt: r, JWS: jws, LeafHash: hex.EncodeToString(leaf)}
	}
	v1 := mk("non_streaming", `{"model":"example-model","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`,
		`{"type":"message","role":"assistant","content":[{"type":"text","text":"hi"}]}`, false, 200)
	// For streams, resp_sha256 covers every byte sent before the trailing
	// ": theophylax-receipt <jws>" comment, which is not itself hashed.
	v2 := mk("streaming", `{"model":"example-model","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hello"}]}`,
		"event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", true, 200)
	v3 := mk("upstream_error", `{"model":"example-model","max_tokens":16,"messages":[]}`,
		`{"type":"error","error":{"type":"invalid_request_error","message":"messages: at least one message is required"}}`, false, 400)

	p := strings.Split(v1.JWS, ".")
	flipped := []byte(p[1])
	if flipped[10] == 'A' {
		flipped[10] = 'B'
	} else {
		flipped[10] = 'A'
	}
	other, _ := NewSigner(nil, bytes.Repeat([]byte{0x07}, 32))
	foreign, _ := other.Sign(v1.Receipt)
	hdrNone := b64.EncodeToString([]byte(`{"alg":"none","kid":"` + s.KID + `","typ":"tpx-receipt+jwt"}`))

	leaves := []string{v1.LeafHash, v2.LeafHash, v3.LeafHash}
	var raw [][]byte
	for _, l := range leaves {
		b, _ := hex.DecodeString(l)
		raw = append(raw, b)
	}
	return vectorFile{
		Comment: "Theophylax receipt test vectors (draft v0). See docs/receipt-spec.md. The signing seed is published for testing only.",
		Seed:    hex.EncodeToString(seed), JWKS: map[string]any{"keys": []any{s.JWK()}}, KID: s.KID,
		Valid: []vector{v1, v2, v3},
		Invalid: []bad{
			{Name: "payload_modified", JWS: p[0] + "." + string(flipped) + "." + p[2], Reason: "signature does not cover the modified payload"},
			{Name: "unknown_key", JWS: foreign, Reason: "signed by a key whose kid is not in the JWKS"},
			{Name: "alg_none", JWS: hdrNone + "." + p[1] + ".", Reason: "only alg EdDSA is accepted"},
		},
		Merkle: merkle{Leaves: leaves, Root: hex.EncodeToString(MerkleRoot(raw))},
	}
}

func TestVectors(t *testing.T) {
	want := buildVectors(t)
	path := "testdata/vectors.json"
	if *update {
		b, _ := json.MarshalIndent(want, "", "  ")
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	var got vectorFile
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	// The published vectors must still match what this implementation produces.
	if got.KID != want.KID || got.Merkle.Root != want.Merkle.Root || len(got.Valid) != len(want.Valid) {
		t.Fatalf("vectors.json is out of date with the implementation; review the format change and rerun with -update")
	}
	jwks, _ := json.Marshal(got.JWKS)
	keys, err := ParseJWKS(jwks)
	if err != nil || len(keys) != 1 {
		t.Fatalf("jwks: %v", err)
	}
	for i, v := range got.Valid {
		if v.JWS != want.Valid[i].JWS {
			t.Errorf("%s: published JWS differs from what the implementation signs", v.Name)
		}
		r, err := Verify(v.JWS, keys)
		if err != nil {
			t.Errorf("%s: %v", v.Name, err)
			continue
		}
		if SHA256Hex([]byte(v.Request)) != r.ReqSHA256 || SHA256Hex([]byte(v.Response)) != r.RespSHA256 {
			t.Errorf("%s: body digests do not match the receipt", v.Name)
		}
		if h := sha256.Sum256([]byte(v.JWS)); hex.EncodeToString(h[:]) != v.LeafHash {
			t.Errorf("%s: leaf hash is not SHA-256 of the JWS", v.Name)
		}
	}
	for _, v := range got.Invalid {
		if _, err := Verify(v.JWS, keys); err == nil {
			t.Errorf("%s: verified, want rejection (%s)", v.Name, v.Reason)
		}
	}
}
