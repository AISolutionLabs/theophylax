package migrate

import (
	"encoding/json"
	"os"
	"testing"
)

// Vectors produced by PyNaCl and pyca/cryptography exactly as LiteLLM does.
func TestDecryptLiteLLM(t *testing.T) {
	b, _ := os.ReadFile("testdata/litellm_encrypted.json")
	var v struct {
		Salt   string
		Values []struct{ Plain, Enc string }
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	for i, x := range v.Values {
		got, err := DecryptLiteLLM(x.Enc, v.Salt)
		if err != nil || got != x.Plain {
			t.Fatalf("value %d: got %q err %v, want %q", i, got, err, x.Plain)
		}
		if _, err := DecryptLiteLLM(x.Enc, "wrong-key"); err == nil {
			t.Fatalf("value %d decrypted with the wrong key", i)
		}
	}
}
