package providers

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Bedrock calls Anthropic models through bedrock-runtime InvokeModel(/WithResponseStream).
//
//	bedrock://<region>?auth=default|irsa|pod_identity|instance|static|assume_role|api_key
//	  &role_arn=..&external_id=..&access_key_id=..&secret_access_key=..
//	  &api_key=<bedrock API key (bearer)>
//	  &endpoint=https://vpce-xxxx.bedrock-runtime.<region>.vpce.amazonaws.com  (PrivateLink)
//	  &control_endpoint=https://bedrock.<region>.amazonaws.com
type Bedrock struct {
	baseProvider
	region, endpoint, control string
	creds                     awsCredSource
	bearer                    string
}

func newBedrock(b baseProvider, c *Conn) (*Bedrock, error) {
	region := c.Host
	if region == "" {
		return nil, fmt.Errorf("bedrock: region is required (bedrock://<region>)")
	}
	p := &Bedrock{baseProvider: b, region: region}
	p.endpoint = strings.TrimRight(c.Get("endpoint"), "/")
	if p.endpoint == "" {
		p.endpoint = "https://bedrock-runtime." + region + ".amazonaws.com"
	}
	p.control = strings.TrimRight(c.Get("control_endpoint"), "/")
	if p.control == "" {
		p.control = "https://bedrock." + region + ".amazonaws.com"
	}
	if c.Params["auth"] == "api_key" || c.Params["auth"] == "bearer" {
		p.bearer = c.Get("api_key")
		if p.bearer == "" {
			return nil, fmt.Errorf("bedrock: auth=api_key requires api_key")
		}
		return p, nil
	}
	cs, err := awsCredsFromConn(c, region, b.client)
	if err != nil {
		return nil, err
	}
	p.creds = cs
	return p, nil
}

func (p *Bedrock) Format() Format { return FormatAnthropic }

func (p *Bedrock) sign(ctx context.Context, req *http.Request, body []byte, service string) error {
	if p.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+p.bearer)
		return nil
	}
	cr, err := p.creds.Retrieve(ctx)
	if err != nil {
		return err
	}
	SignV4(req, body, cr, p.region, service, time.Now())
	return nil
}

func (p *Bedrock) Send(ctx context.Context, r *Request) (*http.Response, error) {
	body := p.prepBody(r.Body)
	delete(body, "model")
	delete(body, "stream")
	if _, ok := body["anthropic_version"]; !ok {
		body["anthropic_version"] = "bedrock-2023-05-31"
	}
	// Beta flags travel in the body on Bedrock.
	if betas := r.Header.Values("anthropic-beta"); len(betas) > 0 {
		if _, ok := body["anthropic_beta"]; !ok {
			var list []string
			for _, b := range betas {
				for _, x := range strings.Split(b, ",") {
					if x = strings.TrimSpace(x); x != "" {
						list = append(list, x)
					}
				}
			}
			body["anthropic_beta"] = list
		}
	}
	payload := marshal(body)
	op := "invoke"
	if r.Stream {
		op = "invoke-with-response-stream"
	}
	u := p.endpoint + "/model/" + bedrockModelPath(r.UpstreamModel) + "/" + op
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.Stream {
		req.Header.Set("Accept", "application/vnd.amazon.eventstream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	p.applyHeaders(req)
	if err := p.sign(ctx, req, payload, "bedrock"); err != nil {
		return nil, fmt.Errorf("bedrock credentials: %w", err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp); err != nil {
		ue := err.(*UpstreamError)
		// Normalise Bedrock error status quirks.
		et := ue.Header.Get("X-Amzn-Errortype")
		if strings.HasPrefix(et, "ThrottlingException") || strings.HasPrefix(et, "ServiceQuotaExceeded") {
			ue.Status = 429
		} else if strings.HasPrefix(et, "ModelNotReady") || strings.HasPrefix(et, "ServiceUnavailable") {
			ue.Status = 503
		}
		return nil, ue
	}
	if r.Stream {
		resp.Body = bedrockStreamToSSE(resp.Body)
		resp.Header.Set("Content-Type", "text/event-stream")
	}
	return resp, nil
}

func (p *Bedrock) ListModels(ctx context.Context) ([]DiscoveredModel, error) {
	var out []DiscoveredModel
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.control+"/foundation-models?byProvider=Anthropic", nil)
	if err := p.sign(ctx, req, nil, "bedrock"); err != nil {
		return nil, err
	}
	var fm struct {
		ModelSummaries []struct {
			ModelID        string   `json:"modelId"`
			ModelName      string   `json:"modelName"`
			InferenceTypes []string `json:"inferenceTypesSupported"`
		} `json:"modelSummaries"`
	}
	if err := getJSON(ctx, p.client, req, &fm); err != nil {
		return nil, err
	}
	for _, m := range fm.ModelSummaries {
		// Models served only through cross-region inference profiles (e.g.
		// Claude Haiku 5.5) cannot be invoked by their bare model ID; offering
		// them would add a deployment that always fails. Their profiles are
		// listed below.
		if len(m.InferenceTypes) > 0 && !slices.Contains(m.InferenceTypes, "ON_DEMAND") {
			continue
		}
		out = append(out, DiscoveredModel{ID: m.ModelID, DisplayName: m.ModelName, Kind: "foundation"})
	}
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.control+"/inference-profiles?maxResults=1000", nil)
	if err := p.sign(ctx, req2, nil, "bedrock"); err == nil {
		var ip struct {
			Summaries []struct {
				ID   string `json:"inferenceProfileId"`
				Name string `json:"inferenceProfileName"`
			} `json:"inferenceProfileSummaries"`
		}
		if getJSON(ctx, p.client, req2, &ip) == nil {
			for _, s := range ip.Summaries {
				if strings.Contains(s.ID, "anthropic") {
					out = append(out, DiscoveredModel{ID: s.ID, DisplayName: s.Name, Kind: "inference_profile"})
				}
			}
		}
	}
	return out, nil
}

// ApplyGuardrail calls Bedrock Guardrails (used by the guardrail engine).
func (p *Bedrock) ApplyGuardrail(ctx context.Context, id, version, source string, texts []string) (blocked bool, output string, err error) {
	content := make([]map[string]any, 0, len(texts))
	for _, t := range texts {
		content = append(content, map[string]any{"text": map[string]any{"text": t}})
	}
	payload := marshal(map[string]any{"source": source, "content": content})
	u := p.endpoint + "/guardrail/" + bedrockModelPath(id) + "/version/" + bedrockModelPath(version) + "/apply"
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if err := p.sign(ctx, req, payload, "bedrock"); err != nil {
		return false, "", err
	}
	var out struct {
		Action  string `json:"action"`
		Outputs []struct {
			Text string `json:"text"`
		} `json:"outputs"`
	}
	if err := getJSON(ctx, p.client, req, &out); err != nil {
		return false, "", err
	}
	var sb strings.Builder
	for _, o := range out.Outputs {
		sb.WriteString(o.Text)
	}
	return out.Action == "GUARDRAIL_INTERVENED", sb.String(), nil
}
