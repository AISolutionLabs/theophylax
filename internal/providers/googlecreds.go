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
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const googleScope = "https://www.googleapis.com/auth/cloud-platform"

// googleToken obtains OAuth access tokens for Google Cloud (Vertex AI).
//
//	auth=service_account   credentials=<path or JSON>   (a service account key file)
//	auth=metadata          GCE / GKE workload identity metadata server
//	auth=workload_identity  workload identity federation from a token file
//	                        audience=//iam.googleapis.com/projects/N/locations/global/workloadIdentityPools/P/providers/X
//	                        token_file=/var/run/secrets/eks.amazonaws.com/serviceaccount/token
//	                        service_account=sa@proj.iam.gserviceaccount.com   (optional impersonation)
//	auth=default (or empty) GOOGLE_APPLICATION_CREDENTIALS if set, otherwise the metadata server
//
// An external_account JSON file (as written by `gcloud iam workload-identity-pools
// create-cred-config`) is also accepted as credentials= with a file credential source.
type googleToken struct {
	mu     sync.Mutex
	tok    string
	exp    time.Time
	client *http.Client
	fetch  func(ctx context.Context) (string, time.Duration, error)
}

func (t *googleToken) Get(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tok != "" && time.Until(t.exp) > 3*time.Minute {
		return t.tok, nil
	}
	tok, ttl, err := t.fetch(ctx)
	if err != nil {
		return "", fmt.Errorf("google token: %w", err)
	}
	if ttl <= 0 {
		ttl = 50 * time.Minute
	}
	t.tok, t.exp = tok, time.Now().Add(ttl)
	return tok, nil
}

type googleCredFile struct {
	Type                           string `json:"type"`
	ClientEmail                    string `json:"client_email"`
	PrivateKey                     string `json:"private_key"`
	PrivateKeyID                   string `json:"private_key_id"`
	TokenURI                       string `json:"token_uri"`
	Audience                       string `json:"audience"`
	SubjectTokenType               string `json:"subject_token_type"`
	ServiceAccountImpersonationURL string `json:"service_account_impersonation_url"`
	CredentialSource               struct {
		File   string `json:"file"`
		Format struct {
			Type                  string `json:"type"`
			SubjectTokenFieldName string `json:"subject_token_field_name"`
		} `json:"format"`
	} `json:"credential_source"`
}

func newGoogleToken(c *Conn, client *http.Client) (*googleToken, error) {
	t := &googleToken{client: client}
	mode := c.Params["auth"]
	creds := c.Get("credentials")
	if mode == "" || mode == "default" {
		if creds == "" {
			creds = os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
		}
		if creds == "" {
			mode = "metadata"
		}
	}
	switch mode {
	case "metadata", "gke", "gce":
		t.fetch = t.metadata
		return t, nil
	case "workload_identity", "federated", "external_account":
		if creds == "" {
			aud, tf := c.Get("audience"), c.Get("token_file")
			if aud == "" || tf == "" {
				return nil, errors.New("vertex workload_identity requires audience and token_file (or credentials= an external_account file)")
			}
			f := &googleCredFile{Type: "external_account", Audience: aud, SubjectTokenType: "urn:ietf:params:oauth:token-type:jwt"}
			f.CredentialSource.File = tf
			if sa := c.Get("service_account"); sa != "" {
				f.ServiceAccountImpersonationURL = "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/" + sa + ":generateAccessToken"
			}
			t.fetch = func(ctx context.Context) (string, time.Duration, error) { return t.external(ctx, f) }
			return t, nil
		}
	case "", "default", "service_account", "json":
	default:
		return nil, fmt.Errorf("unknown vertex auth %q", mode)
	}
	if creds == "" {
		return nil, errors.New("vertex service_account auth requires credentials (a key file path or JSON)")
	}
	raw := []byte(creds)
	if !strings.HasPrefix(strings.TrimSpace(creds), "{") {
		b, err := os.ReadFile(creds)
		if err != nil {
			return nil, fmt.Errorf("google credentials: %w", err)
		}
		raw = b
	}
	var f googleCredFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("google credentials: %w", err)
	}
	switch f.Type {
	case "service_account":
		key, err := parseRSAKey(f.PrivateKey)
		if err != nil {
			return nil, err
		}
		if f.TokenURI == "" {
			f.TokenURI = "https://oauth2.googleapis.com/token"
		}
		t.fetch = func(ctx context.Context) (string, time.Duration, error) { return t.serviceAccount(ctx, &f, key) }
	case "external_account":
		if f.CredentialSource.File == "" {
			return nil, errors.New("google external_account credentials must use a file credential source")
		}
		t.fetch = func(ctx context.Context) (string, time.Duration, error) { return t.external(ctx, &f) }
	default:
		return nil, fmt.Errorf("unsupported google credential type %q", f.Type)
	}
	return t, nil
}

func parseRSAKey(p string) (*rsa.PrivateKey, error) {
	blk, _ := pem.Decode([]byte(p))
	if blk == nil {
		return nil, errors.New("google credentials: private_key is not PEM")
	}
	if k, err := x509.ParsePKCS8PrivateKey(blk.Bytes); err == nil {
		if rk, ok := k.(*rsa.PrivateKey); ok {
			return rk, nil
		}
		return nil, errors.New("google credentials: private_key is not RSA")
	}
	return x509.ParsePKCS1PrivateKey(blk.Bytes)
}

type googleTokenResp struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	// iamcredentials generateAccessToken
	Token      string `json:"accessToken"`
	ExpireTime string `json:"expireTime"`
}

func (r googleTokenResp) result() (string, time.Duration, error) {
	if r.AccessToken != "" {
		return r.AccessToken, time.Duration(r.ExpiresIn) * time.Second, nil
	}
	if r.Token != "" {
		exp, _ := time.Parse(time.RFC3339, r.ExpireTime)
		return r.Token, time.Until(exp), nil
	}
	return "", 0, errors.New("no access token in response")
}

func (t *googleToken) serviceAccount(ctx context.Context, f *googleCredFile, key *rsa.PrivateKey) (string, time.Duration, error) {
	now := time.Now()
	hdr := map[string]string{"alg": "RS256", "typ": "JWT"}
	if f.PrivateKeyID != "" {
		hdr["kid"] = f.PrivateKeyID
	}
	claims := map[string]any{"iss": f.ClientEmail, "scope": googleScope, "aud": f.TokenURI, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	signing := enc(hdr) + "." + enc(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", 0, err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {signing + "." + base64.RawURLEncoding.EncodeToString(sig)}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, f.TokenURI, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out googleTokenResp
	if err := getJSON(ctx, t.client, req, &out); err != nil {
		return "", 0, err
	}
	return out.result()
}

func (t *googleToken) metadata(ctx context.Context) (string, time.Duration, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token", nil)
	req.Header.Set("Metadata-Flavor", "Google")
	var out googleTokenResp
	if err := getJSON(ctx, directClient, req, &out); err != nil {
		return "", 0, err
	}
	return out.result()
}

// external exchanges a third-party OIDC token (EKS, AKS, GitHub…) for a Google
// access token through Security Token Service, then optionally impersonates a
// service account.
func (t *googleToken) external(ctx context.Context, f *googleCredFile) (string, time.Duration, error) {
	b, err := os.ReadFile(f.CredentialSource.File)
	if err != nil {
		return "", 0, fmt.Errorf("subject token: %w", err)
	}
	subject := strings.TrimSpace(string(b))
	if f.CredentialSource.Format.Type == "json" {
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return "", 0, err
		}
		subject, _ = m[f.CredentialSource.Format.SubjectTokenFieldName].(string)
	}
	stt := f.SubjectTokenType
	if stt == "" {
		stt = "urn:ietf:params:oauth:token-type:jwt"
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:token-exchange"}, "audience": {f.Audience},
		"scope": {googleScope}, "requested_token_type": {"urn:ietf:params:oauth:token-type:access_token"},
		"subject_token_type": {stt}, "subject_token": {subject},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://sts.googleapis.com/v1/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out googleTokenResp
	if err := getJSON(ctx, t.client, req, &out); err != nil {
		return "", 0, fmt.Errorf("sts token exchange: %w", err)
	}
	tok, ttl, err := out.result()
	if err != nil || f.ServiceAccountImpersonationURL == "" {
		return tok, ttl, err
	}
	body, _ := json.Marshal(map[string]any{"scope": []string{googleScope}, "lifetime": "3600s"})
	req, _ = http.NewRequestWithContext(ctx, http.MethodPost, f.ServiceAccountImpersonationURL, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	var imp googleTokenResp
	if err := getJSON(ctx, t.client, req, &imp); err != nil {
		return "", 0, fmt.Errorf("service account impersonation: %w", err)
	}
	return imp.result()
}
