package providers

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPresetsAndNewKinds(t *testing.T) {
	cases := map[string]struct{ kind, base string }{
		"groq://?api_key=x":                        {KindOpenAI, "https://api.groq.com/openai/v1"},
		"mistral://?api_key=x":                     {KindOpenAI, "https://api.mistral.ai/v1"},
		"ollama://gpu-box:11434":                   {KindOpenAI, "http://gpu-box:11434/v1"},
		"Provider=DeepSeek;ApiKey=x":               {KindOpenAI, "https://api.deepseek.com/v1"},
		"vllm://?base_url=http://10.0.0.5:8000/v1": {KindOpenAI, "http://10.0.0.5:8000/v1"},
		"gemini://?api_key=x":                      {KindGemini, ""},
		"vertex://us-east5?project=p":              {KindVertex, ""},
		"vertex-openai://us-central1?project=p":    {KindVertexOpenAI, ""},
	}
	for s, want := range cases {
		c, err := ParseConn(s)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if c.Kind != want.kind || c.Params["base_url"] != want.base {
			t.Errorf("%s: got kind=%s base=%q, want %s %q", s, c.Kind, c.Params["base_url"], want.kind, want.base)
		}
	}
	p, err := New("gemini://?api_key=k", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if o := p.(*OpenAI); o.base != "https://generativelanguage.googleapis.com/v1beta/openai" || o.apiKey != "k" {
		t.Fatalf("gemini base %q", o.base)
	}
	if red, _ := ParseConn(`vertex://global?project=p&credentials={"private_key":"secretsecretsecret"}`); strings.Contains(red.Redacted(), "secretsecret") {
		t.Fatalf("credentials not redacted: %s", red.Redacted())
	}
}

func TestVertexServiceAccount(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	var gotBody map[string]any
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			_ = r.ParseForm()
			parts := strings.Split(r.Form.Get("assertion"), ".")
			sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
			sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
				http.Error(w, "bad signature", 401)
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"ya29.test","expires_in":3600}`))
		default:
			if r.Header.Get("Authorization") != "Bearer ya29.test" {
				http.Error(w, "unauthorized", 401)
				return
			}
			gotPath = r.URL.EscapedPath()
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			_, _ = w.Write([]byte(`{"type":"message","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":3,"output_tokens":1}}`))
		}
	}))
	defer srv.Close()
	creds, _ := json.Marshal(map[string]string{"type": "service_account", "client_email": "sa@p.iam.gserviceaccount.com", "private_key": pemKey, "token_uri": srv.URL + "/token"})
	f := filepath.Join(t.TempDir(), "sa.json")
	_ = os.WriteFile(f, creds, 0o600)
	p, err := New("vertex://us-east5?project=my-proj&auth=service_account&credentials="+url.QueryEscape(f)+"&base_url="+url.QueryEscape(srv.URL), Options{})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Send(context.Background(), &Request{Body: map[string]any{"model": "x", "max_tokens": 10, "messages": []any{}}, UpstreamModel: "claude-sonnet-4-5@20250929"})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotPath != "/v1/projects/my-proj/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-5@20250929:rawPredict" {
		t.Fatalf("path %s", gotPath)
	}
	if gotBody["anthropic_version"] != "vertex-2023-10-16" || gotBody["model"] != nil {
		t.Fatalf("body %v", gotBody)
	}
	// Models launched from September 2026 have no "@date" suffix.
	resp, err = p.Send(context.Background(), &Request{Body: map[string]any{"model": "x", "max_tokens": 10, "messages": []any{}}, UpstreamModel: "claude-sonnet-5-5"})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotPath != "/v1/projects/my-proj/locations/us-east5/publishers/anthropic/models/claude-sonnet-5-5:rawPredict" {
		t.Fatalf("path %s", gotPath)
	}
}

func TestBedrockUnsuffixedModelID(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"type":"message","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":3,"output_tokens":1}}`))
	}))
	defer srv.Close()
	p, err := New("bedrock://us-east-1?auth=api_key&api_key=k&endpoint="+url.QueryEscape(srv.URL), Options{})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{
		"global.anthropic.claude-opus-5-5":             "/model/global.anthropic.claude-opus-5-5/invoke",
		"us.anthropic.claude-sonnet-4-5-20250929-v1:0": "/model/us.anthropic.claude-sonnet-4-5-20250929-v1%3A0/invoke",
	} {
		resp, err := p.Send(context.Background(), &Request{Body: map[string]any{"model": "x", "max_tokens": 10, "messages": []any{}}, UpstreamModel: id})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if gotPath != want {
			t.Errorf("%s: path %s, want %s", id, gotPath, want)
		}
	}
}

func TestBedrockEntraFederation(t *testing.T) {
	var sawToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
			if r.Form.Get("scope") != "api://bedrock-federation/.default" {
				http.Error(w, "bad scope "+r.Form.Get("scope"), 400)
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"entra-jwt","expires_in":3600}`))
		case r.Form.Get("Action") == "AssumeRoleWithWebIdentity":
			sawToken = r.Form.Get("WebIdentityToken")
			_, _ = w.Write([]byte(`<AssumeRoleWithWebIdentityResponse><AssumeRoleWithWebIdentityResult><Credentials><AccessKeyId>AKIA</AccessKeyId><SecretAccessKey>s</SecretAccessKey><SessionToken>t</SessionToken><Expiration>2099-01-01T00:00:00Z</Expiration></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`))
		default:
			if !strings.Contains(r.Header.Get("Authorization"), "Credential=AKIA/") {
				http.Error(w, "unsigned", 403)
				return
			}
			_, _ = w.Write([]byte(`{"embedding":[0.1,0.2],"inputTextTokenCount":4}`))
		}
	}))
	defer srv.Close()
	conn := "bedrock://eu-west-1?auth=entra&entra_auth=entra&tenant_id=tid&client_id=cid&client_secret=cs" +
		"&audience=api://bedrock-federation&role_arn=arn:aws:iam::123:role/team-a" +
		"&authority_host=" + url.QueryEscape(srv.URL) + "&sts_endpoint=" + url.QueryEscape(srv.URL+"/") + "&endpoint=" + url.QueryEscape(srv.URL)
	p, err := New(conn, Options{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.(Embedder).Embed(context.Background(), &EmbedRequest{Input: []string{"hello world"}, Body: map[string]any{}, Model: "amazon.titan-embed-text-v2:0"})
	if err != nil {
		t.Fatal(err)
	}
	if sawToken != "entra-jwt" {
		t.Fatalf("STS got token %q", sawToken)
	}
	var out struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	_ = json.Unmarshal(res.Body, &out)
	if res.InputTokens != 4 || len(out.Data) != 1 || len(out.Data[0].Embedding) != 2 {
		t.Fatalf("embed result %s", res.Body)
	}
}

func TestOpenAIEmbedPassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/embeddings" || !strings.Contains(string(b), `"model":"text-embedding-3-small"`) {
			http.Error(w, "bad "+r.URL.Path, 400)
			return
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":7,"total_tokens":7}}`))
	}))
	defer srv.Close()
	p, _ := New("openai://x?api_key=k&base_url="+url.QueryEscape(srv.URL+"/v1"), Options{})
	res, err := p.(Embedder).Embed(context.Background(), &EmbedRequest{Input: []string{"a"}, Body: map[string]any{"input": "a"}, Model: "text-embedding-3-small"})
	if err != nil || res.InputTokens != 7 {
		t.Fatalf("%v %+v", err, res)
	}
}
