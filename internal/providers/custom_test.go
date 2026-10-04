package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeIdP issues tokens and an API that only accepts the current access token.
type fakeIdP struct {
	mu        sync.Mutex
	current   string
	refresh   string
	n         int
	grants    []string
	basicSeen []string
	expiresIn int
	apiCalls  atomic.Int32
	bodies    []string
}

func (f *fakeIdP) server(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			g := r.PostForm.Get("grant_type")
			f.grants = append(f.grants, g)
			if u, p, ok := r.BasicAuth(); ok {
				f.basicSeen = append(f.basicSeen, u+":"+p)
			}
			if g == "refresh_token" && r.PostForm.Get("refresh_token") != f.refresh {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			f.n++
			f.current = "access-" + string(rune('0'+f.n))
			f.refresh = "refresh-" + string(rune('0'+f.n)) // rotated on every use
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": f.current, "refresh_token": f.refresh, "expires_in": f.expiresIn})
		case "/v1/chat/completions":
			f.apiCalls.Add(1)
			b, _ := io.ReadAll(r.Body)
			f.bodies = append(f.bodies, string(b))
			if r.Header.Get("Authorization") != "Bearer "+f.current && !strings.HasPrefix(r.Header.Get("Authorization"), "Basic ") {
				w.WriteHeader(401)
				return
			}
			_, _ = w.Write([]byte(`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o"}]}`))
		}
	}))
}

func send(t *testing.T, p Provider) int {
	t.Helper()
	resp, err := p.Send(context.Background(), &Request{Body: map[string]any{"messages": []any{}}, UpstreamModel: "gpt-4o"})
	if err != nil {
		if ue, ok := err.(*UpstreamError); ok {
			return ue.Status
		}
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestCustomBasicClientCredentials(t *testing.T) {
	f := &fakeIdP{expiresIn: 3600}
	srv := f.server(t)
	defer srv.Close()
	p, err := New("custom://x?name=acme&auth=basic&username=cid&password=s3cret&base_url="+srv.URL+"/v1&token_url="+srv.URL+"/token", Options{ID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if ModelSuffix(p) != "@acme" || p.Kind() != KindCustom {
		t.Fatalf("suffix %q kind %q", ModelSuffix(p), p.Kind())
	}
	if send(t, p) != 200 || send(t, p) != 200 {
		t.Fatal("calls should succeed")
	}
	if len(f.grants) != 1 || f.grants[0] != "client_credentials" || f.basicSeen[0] != "cid:s3cret" {
		t.Fatalf("one client_credentials grant expected, token reused: %v %v", f.grants, f.basicSeen)
	}
	// The upstream revokes the token: the gateway refreshes and retries once, replaying the body.
	f.mu.Lock()
	f.current = "revoked-elsewhere"
	f.mu.Unlock()
	f.apiCalls.Store(0)
	if c := send(t, p); c != 200 || f.apiCalls.Load() != 2 {
		t.Fatalf("retry after 401: status %d calls %d", c, f.apiCalls.Load())
	}
	if last := f.grants[len(f.grants)-1]; last != "refresh_token" {
		t.Fatalf("should use the refresh token first: %v", f.grants)
	}
	if b := f.bodies[len(f.bodies)-1]; !strings.Contains(b, `"model":"gpt-4o"`) {
		t.Fatalf("body not replayed: %q", b)
	}
}

func TestCustomExpiredTokenRefreshesProactively(t *testing.T) {
	f := &fakeIdP{expiresIn: 30} // inside the 1-minute renewal margin
	srv := f.server(t)
	defer srv.Close()
	p, _ := New("custom://x?name=acme&auth=basic&username=cid&password=pw&base_url="+srv.URL+"/v1&token_url="+srv.URL+"/token", Options{ID: "p1"})
	send(t, p)
	send(t, p)
	if len(f.grants) != 2 || f.grants[1] != "refresh_token" || f.apiCalls.Load() != 2 {
		t.Fatalf("expiring token should be refreshed before use: %v calls=%d", f.grants, f.apiCalls.Load())
	}
}

func TestCustomBearerWithRefresh(t *testing.T) {
	f := &fakeIdP{expiresIn: 3600, current: "initial", refresh: "seed-refresh"}
	srv := f.server(t)
	defer srv.Close()
	conn := "custom://x?name=acme&auth=bearer&token=initial&refresh_token=seed-refresh&client_id=app&base_url=" + srv.URL + "/v1&token_url=" + srv.URL + "/token"
	store := &memStore{}
	p, err := New(conn, Options{ID: "p1", Tokens: store})
	if err != nil {
		t.Fatal(err)
	}
	if send(t, p) != 200 || len(f.grants) != 0 {
		t.Fatalf("static token should be used first: %v", f.grants)
	}
	f.mu.Lock()
	f.current = "expired-upstream"
	f.mu.Unlock()
	if send(t, p) != 200 || len(f.grants) != 1 || f.grants[0] != "refresh_token" {
		t.Fatalf("bearer should refresh on 401: %v", f.grants)
	}
	// A second replica (or a restart) picks up the rotated tokens from the shared store
	// instead of reusing the now-invalid seed refresh token.
	p2, _ := New(conn, Options{ID: "p1", Tokens: store})
	if send(t, p2) != 200 || len(f.grants) != 1 {
		t.Fatalf("replica should reuse the stored token: grants=%v", f.grants)
	}
	f.mu.Lock()
	f.current = "revoked"
	f.mu.Unlock()
	if send(t, p2) != 200 || f.grants[len(f.grants)-1] != "refresh_token" || len(f.grants) != 2 {
		t.Fatalf("replica should refresh with the rotated refresh token: %v", f.grants)
	}
}

func TestCustomStaticBearerAndBasic(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	p, _ := New("custom://x?name=acme&auth=bearer&token=tok123&base_url="+srv.URL, Options{})
	send(t, p)
	if auth != "Bearer tok123" {
		t.Fatalf("bearer: %q", auth)
	}
	p, _ = New("custom://x?name=acme&auth=basic&username=u&password=p&format=anthropic&base_url="+srv.URL, Options{})
	if p.Format() != FormatAnthropic {
		t.Fatal("anthropic format")
	}
	send(t, p)
	if auth != "Basic dTpw" {
		t.Fatalf("basic: %q", auth)
	}
}

func TestCustomValidation(t *testing.T) {
	for conn, want := range map[string]string{
		"custom://api.x.com?auth=bearer&token=t":                                                   "name is required",
		"custom://api.x.com?name=Acme Corp&auth=bearer&token=t":                                    "name is required",
		"custom://api.x.com?name=acme&auth=basic&username=u":                                       "username and password",
		"custom://api.x.com?name=acme&auth=bearer":                                                 "token and/or a refresh_token",
		"custom://api.x.com?name=acme&auth=bearer&refresh_token=r":                                 "needs token_url",
		"custom://api.x.com?name=acme&auth=digest&token=t":                                         "auth must be basic, bearer or oauth",
		"openai://api.x.com?auth=oauth&client_id=c":                                                "token_url and client_id",
		"openai://api.x.com?auth=oauth&token_url=https://i/t&client_id=c":                          "need client_secret",
		"anthropic://x?auth=oauth&token_url=https://i/t&client_id=c&grant_type=authorization_code": "needs authorize_url",
		"openai://x?auth=oauth&token_url=https://i/t&client_id=c&client_secret=s&client_auth=jwt":  "client_auth must be",
		"custom://api.x.com?name=acme&auth=bearer&token=t&format=gemini":                           "format must be",
	} {
		if _, err := New(conn, Options{}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", conn, err, want)
		}
	}
	c, _ := ParseConn("custom://api.x.com?name=acme&auth=bearer&token=supersecrettoken&refresh_token=anothersecret")
	if r := c.Redacted(); strings.Contains(r, "supersecret") || strings.Contains(r, "anothersecret") {
		t.Fatalf("secrets leaked: %s", r)
	}
	if ModelSuffixFor("custom://api.x.com?name=acme&auth=bearer&token=t") != "@acme" || ModelSuffixFor("openai://api.openai.com") != "" {
		t.Fatal("ModelSuffixFor")
	}
}

type memStore struct {
	mu sync.Mutex
	t  map[string]*Tokens
}

func (m *memStore) Load(_ context.Context, id string) (*Tokens, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t := m.t[id]; t != nil {
		c := *t
		return &c, nil
	}
	return nil, nil
}

func (m *memStore) Update(ctx context.Context, id string, fn func(*Tokens) (*Tokens, error)) (*Tokens, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var cur *Tokens
	if t := m.t[id]; t != nil {
		c := *t
		cur = &c
	}
	next, err := fn(cur)
	if err != nil {
		return nil, err
	}
	if m.t == nil {
		m.t = map[string]*Tokens{}
	}
	c := *next
	m.t[id] = &c
	return next, nil
}

func TestOAuthClientCredentials(t *testing.T) {
	for _, mode := range []string{"basic", "post"} {
		var form, basicUser string
		var mu sync.Mutex
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			if r.URL.Path == "/token" {
				_ = r.ParseForm()
				form = r.PostForm.Encode()
				basicUser, _, _ = r.BasicAuth()
				_, _ = w.Write([]byte(`{"access_token":"cc-tok","expires_in":3600}`))
				return
			}
			if r.Header.Get("Authorization") != "Bearer cc-tok" {
				w.WriteHeader(401)
				return
			}
			_, _ = w.Write([]byte(`{}`))
		}))
		p, err := New("openai://x?auth=oauth&client_id=gw&client_secret=s3&scope=models&client_auth="+mode+"&token_url="+srv.URL+"/token&base_url="+srv.URL+"/v1", Options{})
		if err != nil {
			t.Fatal(err)
		}
		if c := send(t, p); c != 200 {
			t.Fatalf("%s: status %d", mode, c)
		}
		mu.Lock()
		if !strings.Contains(form, "grant_type=client_credentials") || !strings.Contains(form, "scope=models") {
			t.Fatalf("%s: form %s", mode, form)
		}
		if mode == "basic" && (basicUser != "gw" || strings.Contains(form, "client_secret")) {
			t.Fatalf("basic client auth: user=%q form=%s", basicUser, form)
		}
		if mode == "post" && (basicUser != "" || !strings.Contains(form, "client_secret=s3")) {
			t.Fatalf("post client auth: user=%q form=%s", basicUser, form)
		}
		mu.Unlock()
		srv.Close()
	}
}

func TestOAuthAuthorizationCode(t *testing.T) {
	var mu sync.Mutex
	var grants []string
	access := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/token" {
			_ = r.ParseForm()
			g := r.PostForm.Get("grant_type")
			grants = append(grants, g)
			if g == "authorization_code" && (r.PostForm.Get("code") != "the-code" || r.PostForm.Get("code_verifier") != "the-verifier" || r.PostForm.Get("client_id") != "pub") {
				w.WriteHeader(400)
				return
			}
			access = "ac-" + g
			_, _ = w.Write([]byte(`{"access_token":"` + access + `","refresh_token":"rt","expires_in":3600}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+access {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	conn := "anthropic://x?auth=oauth&grant_type=authorization_code&client_id=pub&scope=read&authorize_url=" + srv.URL + "/authorize&token_url=" + srv.URL + "/token&base_url=" + srv.URL
	store := &memStore{}
	p, err := New(conn, Options{ID: "prv", Tokens: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Send(context.Background(), &Request{Body: map[string]any{}, UpstreamModel: "m"}); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("before connecting: %v", err)
	}

	app, err := OAuthAppFor(conn)
	if err != nil || app == nil || app.Grant() != "authorization_code" {
		t.Fatalf("app: %v %v", app, err)
	}
	u := app.AuthCodeURL("st", "chal", "https://gw/cb")
	for _, want := range []string{"/authorize?", "client_id=pub", "code_challenge=chal", "code_challenge_method=S256", "redirect_uri=https%3A%2F%2Fgw%2Fcb", "scope=read", "state=st"} {
		if !strings.Contains(u, want) {
			t.Fatalf("authorize URL %s missing %s", u, want)
		}
	}
	tok, err := app.Exchange(context.Background(), "the-code", "the-verifier", "https://gw/cb")
	if err != nil {
		t.Fatal(err)
	}
	tok.Seed = app.Seed()
	_, _ = store.Update(context.Background(), "prv", func(*Tokens) (*Tokens, error) { return tok, nil })

	if c := send(t, p); c != 200 {
		t.Fatalf("after connecting: %d", c)
	}
	mu.Lock()
	access = "rotated-elsewhere"
	mu.Unlock()
	if c := send(t, p); c != 200 || grants[len(grants)-1] != "refresh_token" {
		t.Fatalf("refresh after 401: %d %v", c, grants)
	}
	if app2, _ := OAuthAppFor("openai://api.openai.com?api_key=k"); app2 != nil {
		t.Fatal("non-oauth connection has no OAuth app")
	}
}
