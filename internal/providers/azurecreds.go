package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// azureToken obtains Entra ID bearer tokens via client secret, workload identity
// federation (AKS, or EKS with a federated credential) or managed identity.
//
//	auth=api_key        api_key=...
//	auth=entra          tenant_id, client_id, client_secret
//	auth=workload       tenant_id, client_id, token_file (defaults to AZURE_* env)
//	auth=managed        [client_id] for a user-assigned identity
type azureToken struct {
	mode, tenant, clientID, secret, tokenFile, scope, authority string
	client                                                      *http.Client
	mu                                                          sync.Mutex
	tok                                                         string
	exp                                                         time.Time
}

func newAzureAuth(c *Conn, client *http.Client) (apiKey string, tok *azureToken, err error) {
	auth := c.Params["auth"]
	if auth == "" {
		if c.Get("api_key") != "" {
			auth = "api_key"
		} else if c.Get("client_secret") != "" {
			auth = "entra"
		} else if os.Getenv("AZURE_FEDERATED_TOKEN_FILE") != "" {
			auth = "workload"
		} else {
			auth = "managed"
		}
	}
	scope := c.Get("scope")
	if scope == "" {
		scope = "https://cognitiveservices.azure.com/.default"
	}
	authority := c.Get("authority_host")
	if authority == "" {
		authority = os.Getenv("AZURE_AUTHORITY_HOST")
	}
	if authority == "" {
		authority = "https://login.microsoftonline.com/"
	}
	t := &azureToken{scope: scope, authority: strings.TrimRight(authority, "/") + "/", client: client}
	or := func(v, env string) string {
		if v != "" {
			return v
		}
		return os.Getenv(env)
	}
	switch auth {
	case "api_key", "key":
		k := c.Get("api_key")
		if k == "" {
			return "", nil, errors.New("azure: api_key required")
		}
		return k, nil, nil
	case "entra", "client_secret", "service_principal":
		t.mode = "secret"
		t.tenant, t.clientID, t.secret = or(c.Get("tenant_id"), "AZURE_TENANT_ID"), or(c.Get("client_id"), "AZURE_CLIENT_ID"), or(c.Get("client_secret"), "AZURE_CLIENT_SECRET")
		if t.tenant == "" || t.clientID == "" || t.secret == "" {
			return "", nil, errors.New("azure entra auth requires tenant_id, client_id, client_secret")
		}
	case "workload", "workload_identity", "federated":
		t.mode = "workload"
		t.tenant, t.clientID, t.tokenFile = or(c.Get("tenant_id"), "AZURE_TENANT_ID"), or(c.Get("client_id"), "AZURE_CLIENT_ID"), or(c.Get("token_file"), "AZURE_FEDERATED_TOKEN_FILE")
		if t.tenant == "" || t.clientID == "" || t.tokenFile == "" {
			return "", nil, errors.New("azure workload identity requires tenant_id, client_id, token_file")
		}
	case "managed", "managed_identity", "msi":
		t.mode = "managed"
		t.clientID = c.Get("client_id")
	default:
		return "", nil, fmt.Errorf("unknown azure auth %q", auth)
	}
	return "", t, nil
}

func (t *azureToken) Get(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tok != "" && time.Until(t.exp) > 5*time.Minute {
		return t.tok, nil
	}
	var out struct {
		AccessToken string      `json:"access_token"`
		ExpiresIn   json_number `json:"expires_in"`
	}
	var req *http.Request
	client := t.client
	switch t.mode {
	case "secret", "workload":
		form := url.Values{"client_id": {t.clientID}, "scope": {t.scope}, "grant_type": {"client_credentials"}}
		if t.mode == "secret" {
			form.Set("client_secret", t.secret)
		} else {
			b, err := os.ReadFile(t.tokenFile)
			if err != nil {
				return "", err
			}
			form.Set("client_assertion", strings.TrimSpace(string(b)))
			form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
		}
		req, _ = http.NewRequestWithContext(ctx, http.MethodPost, t.authority+t.tenant+"/oauth2/v2.0/token", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	case "managed":
		q := url.Values{"api-version": {"2018-02-01"}, "resource": {strings.TrimSuffix(t.scope, "/.default")}}
		if t.clientID != "" {
			q.Set("client_id", t.clientID)
		}
		req, _ = http.NewRequestWithContext(ctx, http.MethodGet, "http://169.254.169.254/metadata/identity/oauth2/token?"+q.Encode(), nil)
		req.Header.Set("Metadata", "true")
		client = directClient
	}
	if err := getJSON(ctx, client, req, &out); err != nil {
		return "", fmt.Errorf("azure token: %w", err)
	}
	t.tok = out.AccessToken
	t.exp = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	return t.tok, nil
}

// json_number accepts expires_in as either a number or a numeric string (IMDS returns a string).
type json_number int64

func (n *json_number) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	var v int64
	_, err := fmt.Sscan(s, &v)
	*n = json_number(v)
	return err
}
