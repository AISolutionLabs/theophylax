package providers

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type AWSCreds struct {
	AccessKey, Secret, Token string
	Expires                  time.Time
}

type awsCredSource interface {
	Retrieve(ctx context.Context) (AWSCreds, error)
}

// cachedCreds refreshes credentials five minutes before expiry.
type cachedCreds struct {
	src awsCredSource
	mu  sync.Mutex
	cur AWSCreds
}

func (c *cachedCreds) Retrieve(ctx context.Context) (AWSCreds, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur.AccessKey != "" && (c.cur.Expires.IsZero() || time.Until(c.cur.Expires) > 5*time.Minute) {
		return c.cur, nil
	}
	cr, err := c.src.Retrieve(ctx)
	if err != nil {
		return AWSCreds{}, err
	}
	c.cur = cr
	return cr, nil
}

type staticCreds AWSCreds

func (s staticCreds) Retrieve(context.Context) (AWSCreds, error) { return AWSCreds(s), nil }

type envCreds struct{}

func (envCreds) Retrieve(context.Context) (AWSCreds, error) {
	ak, sk := os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY")
	if ak == "" || sk == "" {
		return AWSCreds{}, errors.New("AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY not set")
	}
	return AWSCreds{AccessKey: ak, Secret: sk, Token: os.Getenv("AWS_SESSION_TOKEN")}, nil
}

type stsResult struct {
	Credentials struct {
		AccessKeyId     string
		SecretAccessKey string
		SessionToken    string
		Expiration      time.Time
	}
}

func stsEndpoint(region string) string {
	if region == "" {
		return "https://sts.amazonaws.com/"
	}
	return "https://sts." + region + ".amazonaws.com/"
}

// webIdentityCreds implements IRSA (AssumeRoleWithWebIdentity with a projected SA token).
type webIdentityCreds struct {
	roleARN, tokenFile, session, region string
	client                              *http.Client
}

func (w *webIdentityCreds) Retrieve(ctx context.Context) (AWSCreds, error) {
	tok, err := os.ReadFile(w.tokenFile)
	if err != nil {
		return AWSCreds{}, fmt.Errorf("web identity token: %w", err)
	}
	q := url.Values{
		"Action": {"AssumeRoleWithWebIdentity"}, "Version": {"2011-06-15"},
		"RoleArn": {w.roleARN}, "RoleSessionName": {w.session}, "WebIdentityToken": {strings.TrimSpace(string(tok))},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, stsEndpoint(w.region), strings.NewReader(q.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out struct {
		Result stsResult `xml:"AssumeRoleWithWebIdentityResult"`
	}
	if err := doXML(w.client, req, &out); err != nil {
		return AWSCreds{}, fmt.Errorf("sts AssumeRoleWithWebIdentity: %w", err)
	}
	c := out.Result.Credentials
	return AWSCreds{AccessKey: c.AccessKeyId, Secret: c.SecretAccessKey, Token: c.SessionToken, Expires: c.Expiration}, nil
}

// assumeRoleCreds chains sts:AssumeRole on top of a base credential source.
type assumeRoleCreds struct {
	base                                 awsCredSource
	roleARN, externalID, session, region string
	client                               *http.Client
	duration                             int
}

func (a *assumeRoleCreds) Retrieve(ctx context.Context) (AWSCreds, error) {
	base, err := a.base.Retrieve(ctx)
	if err != nil {
		return AWSCreds{}, err
	}
	q := url.Values{"Action": {"AssumeRole"}, "Version": {"2011-06-15"}, "RoleArn": {a.roleARN}, "RoleSessionName": {a.session}}
	if a.externalID != "" {
		q.Set("ExternalId", a.externalID)
	}
	if a.duration > 0 {
		q.Set("DurationSeconds", fmt.Sprint(a.duration))
	}
	body := []byte(q.Encode())
	region := a.region
	if region == "" {
		region = "us-east-1"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, stsEndpoint(a.region), strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	SignV4(req, body, base, region, "sts", time.Now())
	var out struct {
		Result stsResult `xml:"AssumeRoleResult"`
	}
	if err := doXML(a.client, req, &out); err != nil {
		return AWSCreds{}, fmt.Errorf("sts AssumeRole: %w", err)
	}
	c := out.Result.Credentials
	return AWSCreds{AccessKey: c.AccessKeyId, Secret: c.SecretAccessKey, Token: c.SessionToken, Expires: c.Expiration}, nil
}

func doXML(c *http.Client, req *http.Request, out any) error {
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return xml.Unmarshal(b, out)
}

// containerCreds implements EKS Pod Identity and ECS task roles.
type containerCreds struct{}

func (containerCreds) Retrieve(ctx context.Context) (AWSCreds, error) {
	u := os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI")
	if u == "" {
		if rel := os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"); rel != "" {
			u = "http://169.254.170.2" + rel
		}
	}
	if u == "" {
		return AWSCreds{}, errors.New("no container credentials endpoint")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	tok := os.Getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN")
	if f := os.Getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE"); f != "" {
		if b, err := os.ReadFile(f); err == nil {
			tok = strings.TrimSpace(string(b))
		}
	}
	if tok != "" {
		req.Header.Set("Authorization", tok)
	}
	var out struct {
		AccessKeyId, SecretAccessKey, Token string
		Expiration                          time.Time
	}
	if err := getJSON(ctx, directClient, req, &out); err != nil {
		return AWSCreds{}, fmt.Errorf("container credentials: %w", err)
	}
	return AWSCreds{AccessKey: out.AccessKeyId, Secret: out.SecretAccessKey, Token: out.Token, Expires: out.Expiration}, nil
}

// imdsCreds implements EC2 instance profile credentials via IMDSv2.
type imdsCreds struct{}

func (imdsCreds) Retrieve(ctx context.Context) (AWSCreds, error) {
	const base = "http://169.254.169.254/latest"
	treq, _ := http.NewRequestWithContext(ctx, http.MethodPut, base+"/api/token", nil)
	treq.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "21600")
	tresp, err := directClient.Do(treq)
	if err != nil {
		return AWSCreds{}, fmt.Errorf("imds: %w", err)
	}
	tb, _ := io.ReadAll(tresp.Body)
	tresp.Body.Close()
	tok := string(tb)
	get := func(p string) ([]byte, error) {
		r, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+p, nil)
		r.Header.Set("X-aws-ec2-metadata-token", tok)
		resp, err := directClient.Do(r)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("imds %s: %d", p, resp.StatusCode)
		}
		return io.ReadAll(resp.Body)
	}
	role, err := get("/meta-data/iam/security-credentials/")
	if err != nil {
		return AWSCreds{}, err
	}
	b, err := get("/meta-data/iam/security-credentials/" + strings.TrimSpace(strings.Split(string(role), "\n")[0]))
	if err != nil {
		return AWSCreds{}, err
	}
	var out struct {
		AccessKeyId, SecretAccessKey, Token string
		Expiration                          time.Time
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return AWSCreds{}, err
	}
	return AWSCreds{AccessKey: out.AccessKeyId, Secret: out.SecretAccessKey, Token: out.Token, Expires: out.Expiration}, nil
}

// defaultChain mirrors the AWS SDK default chain: env, IRSA, pod identity/ECS, IMDS.
type defaultChain struct {
	region string
	client *http.Client
}

func (d defaultChain) Retrieve(ctx context.Context) (AWSCreds, error) {
	var errs []string
	if c, err := (envCreds{}).Retrieve(ctx); err == nil {
		return c, nil
	}
	if arn, f := os.Getenv("AWS_ROLE_ARN"), os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE"); arn != "" && f != "" {
		c, err := (&webIdentityCreds{roleARN: arn, tokenFile: f, session: sessionName(), region: d.region, client: d.client}).Retrieve(ctx)
		if err == nil {
			return c, nil
		}
		errs = append(errs, err.Error())
	}
	if os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI") != "" || os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI") != "" {
		c, err := (containerCreds{}).Retrieve(ctx)
		if err == nil {
			return c, nil
		}
		errs = append(errs, err.Error())
	}
	c, err := (imdsCreds{}).Retrieve(ctx)
	if err == nil {
		return c, nil
	}
	errs = append(errs, err.Error())
	return AWSCreds{}, fmt.Errorf("no AWS credentials found in default chain: %s", strings.Join(errs, "; "))
}

func sessionName() string {
	if s := os.Getenv("AWS_ROLE_SESSION_NAME"); s != "" {
		return s
	}
	h, _ := os.Hostname()
	if h == "" {
		h = "theophylax"
	}
	if len(h) > 50 {
		h = h[:50]
	}
	return "theophylax-" + h
}

// awsCredsFromConn picks a credential source by auth=
//
//	default | irsa | pod_identity | instance | static | assume_role
func awsCredsFromConn(c *Conn, region string, client *http.Client) (awsCredSource, error) {
	auth := c.Params["auth"]
	var base awsCredSource
	switch auth {
	case "", "default", "chain":
		base = defaultChain{region: region, client: client}
	case "irsa", "web_identity":
		arn := c.Get("role_arn")
		if arn == "" {
			arn = os.Getenv("AWS_ROLE_ARN")
		}
		tf := c.Get("token_file")
		if tf == "" {
			tf = os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE")
		}
		if arn == "" || tf == "" {
			return nil, errors.New("irsa: role_arn/AWS_ROLE_ARN and token_file/AWS_WEB_IDENTITY_TOKEN_FILE are required")
		}
		return &cachedCreds{src: &webIdentityCreds{roleARN: arn, tokenFile: tf, session: sessionName(), region: region, client: client}}, nil
	case "pod_identity", "container":
		base = containerCreds{}
	case "instance", "imds":
		base = imdsCreds{}
	case "static", "keys", "access_key":
		ak, sk := c.Get("access_key_id"), c.Get("secret_access_key")
		if ak == "" || sk == "" {
			return nil, errors.New("static auth requires access_key_id and secret_access_key")
		}
		base = staticCreds{AccessKey: ak, Secret: sk, Token: c.Get("session_token")}
	case "assume_role":
		if c.Get("access_key_id") != "" {
			base = staticCreds{AccessKey: c.Get("access_key_id"), Secret: c.Get("secret_access_key"), Token: c.Get("session_token")}
		} else {
			base = &cachedCreds{src: defaultChain{region: region, client: client}}
		}
		if c.Get("role_arn") == "" {
			return nil, errors.New("assume_role requires role_arn")
		}
		return &cachedCreds{src: &assumeRoleCreds{base: base, roleARN: c.Get("role_arn"), externalID: c.Get("external_id"), session: sessionName(), region: region, client: client}}, nil
	default:
		return nil, fmt.Errorf("unknown bedrock auth %q", auth)
	}
	// Optional role chaining on top of any base source.
	if arn := c.Get("role_arn"); arn != "" {
		return &cachedCreds{src: &assumeRoleCreds{base: &cachedCreds{src: base}, roleARN: arn, externalID: c.Get("external_id"), session: sessionName(), region: region, client: client}}, nil
	}
	return &cachedCreds{src: base}, nil
}
