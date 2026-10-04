package migrate

import "testing"

func FuzzParseYAML(f *testing.F) {
	for _, s := range []string{"a: 1\nb: [x, 'y', \"z\"]\n", "model_list:\n  - model_name: x\n    litellm_params: {model: a/b}\n", "- - x\n  - y\n", "k: |\n  text\n", "{", "a: [1, {b: 2}]"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = parseYAML(s)
		_, _ = ParseConfig(s)
	})
}

func FuzzDecryptLiteLLM(f *testing.F) {
	f.Add("v2:gcm:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA==", "k")
	f.Add("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "k")
	f.Fuzz(func(t *testing.T, v, k string) { _, _ = DecryptLiteLLM(v, k) })
}
