package providers

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Conn is a parsed provider connection string. Two syntaxes are accepted:
//
//	URI:        bedrock://us-east-1?auth=irsa&endpoint=https://vpce-...
//	            anthropic://api.anthropic.com?api_key=${env:ANTHROPIC_API_KEY}
//	            azure-foundry://my-res.services.ai.azure.com?auth=entra&tenant_id=..&client_id=..&client_secret=..
//	Key/value:  Provider=AzureOpenAI;Endpoint=https://x.openai.azure.com;Auth=ApiKey;ApiKey=...
//
// Values may reference secrets indirectly: ${env:NAME} or ${file:/path/to/secret}.
type Conn struct {
	Kind   string
	Host   string
	Params map[string]string
}

var kindAliases = map[string]string{
	"anthropic": KindAnthropic, "claude": KindAnthropic,
	"bedrock": KindBedrock, "aws": KindBedrock, "awsbedrock": KindBedrock, "aws-bedrock": KindBedrock,
	"azure-foundry": KindAzureFoundry, "azurefoundry": KindAzureFoundry, "foundry": KindAzureFoundry, "azure-anthropic": KindAzureFoundry, "azure_foundry": KindAzureFoundry,
	"azure-openai": KindAzureOpenAI, "azureopenai": KindAzureOpenAI, "azure": KindAzureOpenAI, "azure_openai": KindAzureOpenAI,
	"openai": KindOpenAI, "openai-compatible": KindOpenAI, "openai_compatible": KindOpenAI,
	"mock": KindMock,
}

const (
	KindAnthropic    = "anthropic"
	KindBedrock      = "bedrock"
	KindAzureFoundry = "azure-foundry"
	KindAzureOpenAI  = "azure-openai"
	KindOpenAI       = "openai"
	KindMock         = "mock"
)

func normKey(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	k = strings.ReplaceAll(k, "-", "_")
	// CamelCase -> snake for common key/value style keys.
	switch k {
	case "apikey":
		return "api_key"
	case "accesskeyid":
		return "access_key_id"
	case "secretaccesskey":
		return "secret_access_key"
	case "sessiontoken":
		return "session_token"
	case "rolearn":
		return "role_arn"
	case "externalid":
		return "external_id"
	case "tenantid":
		return "tenant_id"
	case "clientid":
		return "client_id"
	case "clientsecret":
		return "client_secret"
	case "apiversion":
		return "api_version"
	case "baseurl":
		return "base_url"
	case "authtoken":
		return "auth_token"
	case "cabundle":
		return "ca_bundle"
	case "authorityhost":
		return "authority_host"
	}
	return k
}

func ParseConn(s string) (*Conn, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty connection string")
	}
	c := &Conn{Params: map[string]string{}}
	if i := strings.Index(s, "://"); i > 0 && !strings.Contains(s[:i], ";") && !strings.Contains(s[:i], "=") {
		u, err := url.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("invalid connection URI: %w", err)
		}
		k, ok := kindAliases[strings.ToLower(u.Scheme)]
		if !ok {
			return nil, fmt.Errorf("unknown provider kind %q", u.Scheme)
		}
		c.Kind = k
		c.Host = u.Host + strings.TrimRight(u.Path, "/")
		for key, vals := range u.Query() {
			if len(vals) > 0 {
				c.Params[normKey(key)] = vals[0]
			}
		}
		if u.User != nil {
			if p, ok := u.User.Password(); ok {
				c.Params["api_key"] = p
			} else if u.User.Username() != "" {
				c.Params["api_key"] = u.User.Username()
			}
		}
	} else {
		for _, part := range strings.Split(s, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			kv := strings.SplitN(part, "=", 2)
			if len(kv) != 2 {
				return nil, fmt.Errorf("invalid segment %q (expected key=value)", part)
			}
			c.Params[normKey(kv[0])] = strings.TrimSpace(kv[1])
		}
		p := strings.ToLower(c.Params["provider"])
		k, ok := kindAliases[p]
		if !ok {
			return nil, fmt.Errorf("connection string must include Provider=<anthropic|bedrock|azure-foundry|azure-openai|openai>")
		}
		c.Kind = k
		delete(c.Params, "provider")
		for _, hk := range []string{"region", "endpoint", "host", "resource"} {
			if v := c.Params[hk]; v != "" && c.Host == "" {
				c.Host = strings.TrimRight(strings.TrimPrefix(strings.TrimPrefix(v, "https://"), "http://"), "/")
				if hk == "region" {
					delete(c.Params, "region")
				}
				if hk == "endpoint" && c.Kind != KindBedrock {
					delete(c.Params, "endpoint")
				}
			}
		}
	}
	if c.Kind == KindBedrock && c.Host == "" {
		c.Host = c.Params["region"]
	}
	if c.Params["auth"] != "" {
		c.Params["auth"] = strings.ToLower(strings.ReplaceAll(c.Params["auth"], "-", "_"))
	}
	return c, nil
}

var refRe = regexp.MustCompile(`\$\{(env|file):([^}]+)\}`)

// Get returns a parameter with ${env:} / ${file:} references resolved.
func (c *Conn) Get(k string) string {
	v := c.Params[k]
	return resolveRefs(v)
}

func resolveRefs(v string) string {
	return refRe.ReplaceAllStringFunc(v, func(m string) string {
		sm := refRe.FindStringSubmatch(m)
		switch sm[1] {
		case "env":
			return os.Getenv(sm[2])
		case "file":
			b, err := os.ReadFile(sm[2])
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(b))
		}
		return ""
	})
}

var secretKeys = map[string]bool{"api_key": true, "secret_access_key": true, "session_token": true, "client_secret": true, "auth_token": true, "password": true, "token": true}

// Redacted renders the connection with secret values masked (references are kept visible).
func (c *Conn) Redacted() string {
	keys := make([]string, 0, len(c.Params))
	for k := range c.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	q := make([]string, 0, len(keys))
	for _, k := range keys {
		v := c.Params[k]
		if secretKeys[k] && !strings.HasPrefix(v, "${") {
			if len(v) > 8 {
				v = "****" + v[len(v)-4:]
			} else {
				v = "****"
			}
		}
		q = append(q, k+"="+v)
	}
	s := c.Kind + "://" + c.Host
	if len(q) > 0 {
		s += "?" + strings.Join(q, "&")
	}
	return s
}

// HTTPClient builds a tuned client honouring per-connection proxy and CA settings
// (useful behind TLS-inspecting egress such as Zscaler).
func (c *Conn) HTTPClient(timeout time.Duration) (*http.Client, error) {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          1024,
		MaxIdleConnsPerHost:   256,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
	}
	switch p := c.Get("proxy"); p {
	case "", "env":
	case "none", "direct":
		tr.Proxy = nil
	default:
		pu, err := url.Parse(p)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy: %w", err)
		}
		tr.Proxy = http.ProxyURL(pu)
	}
	if ca := c.Get("ca_bundle"); ca != "" {
		pem := []byte(ca)
		if !strings.Contains(ca, "BEGIN CERTIFICATE") {
			b, err := os.ReadFile(ca)
			if err != nil {
				return nil, fmt.Errorf("ca_bundle: %w", err)
			}
			pem = b
		}
		pool, _ := x509.SystemCertPool()
		if pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ca_bundle: no certificates found")
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Transport: tr, Timeout: timeout}, nil
}

// directClient is used for link-local metadata endpoints (IMDS, pod identity) — never proxied.
var directClient = &http.Client{
	Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext},
	Timeout:   5 * time.Second,
}
