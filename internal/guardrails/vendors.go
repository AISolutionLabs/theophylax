package guardrails

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// Vendor guardrails call hosted classifiers. Credentials in rules should be
// references (${env:NAME} or ${file:/path}) so policies never hold secrets.
//
//	lakera                Lakera Guard v2 (prompt attacks, PII, moderation per your Lakera policy)
//	azure_prompt_shield   Azure AI Content Safety Prompt Shields (direct and indirect injection)
//	azure_content_safety  Azure AI Content Safety text analysis (hate, sexual, violence, self-harm)
//	openai_moderation     OpenAI omni-moderation (or any compatible /moderations endpoint)

var refRe = regexp.MustCompile(`\$\{(env|file):([^}]+)\}`)

func resolveRef(s string) string {
	return refRe.ReplaceAllStringFunc(s, func(m string) string {
		p := refRe.FindStringSubmatch(m)
		if p[1] == "env" {
			return os.Getenv(p[2])
		}
		b, err := os.ReadFile(p[2])
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	})
}

func (e *Engine) vendorCall(ctx context.Context, r Rule, url string, body any, hdr map[string]string, out any) error {
	timeout := time.Duration(r.TimeoutMS) * time.Millisecond
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	for k, v := range r.Headers {
		req.Header.Set(k, resolveRef(v))
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(b), 200))
	}
	return json.Unmarshal(b, out)
}

func (e *Engine) vendor(ctx context.Context, p Policy, r Rule, in Input, texts []TextRef, res *Result, block func(string)) {
	var sys, users, tools []string
	var latestUser string
	for _, t := range texts {
		s := t.Get()
		if s == "" {
			continue
		}
		switch t.Role {
		case "system", "developer":
			sys = append(sys, s)
		case "tool":
			tools = append(tools, s)
		default:
			users = append(users, s)
			if t.Latest {
				latestUser = s
			}
		}
	}
	if len(users)+len(tools) == 0 {
		return
	}
	if latestUser == "" && len(users) > 0 {
		latestUser = users[len(users)-1]
	}
	key := resolveRef(r.APIKey)
	var flagged bool
	var why string
	var err error
	switch r.Type {
	case "lakera":
		url := orDefault(r.URL, "https://api.lakera.ai/v2/guard")
		msgs := []map[string]string{}
		for _, s := range sys {
			msgs = append(msgs, map[string]string{"role": "system", "content": s})
		}
		role := "user"
		if in.Phase == "output" {
			role = "assistant"
		}
		for _, s := range append(users, tools...) {
			msgs = append(msgs, map[string]string{"role": role, "content": s})
		}
		body := map[string]any{"messages": msgs, "breakdown": true}
		if r.ProjectID != "" {
			body["project_id"] = r.ProjectID
		}
		var out struct {
			Flagged   bool `json:"flagged"`
			Breakdown []struct {
				DetectorType string `json:"detector_type"`
				Detected     bool   `json:"detected"`
			} `json:"breakdown"`
		}
		err = e.vendorCall(ctx, r, url, body, map[string]string{"Authorization": "Bearer " + key}, &out)
		flagged = out.Flagged
		for _, b := range out.Breakdown {
			if b.Detected {
				why = b.DetectorType
				break
			}
		}
	case "azure_prompt_shield":
		if in.Phase == "output" {
			return
		}
		docs := tools
		if len(docs) > 5 {
			docs = docs[len(docs)-5:]
		}
		var out struct {
			UserPromptAnalysis struct {
				AttackDetected bool `json:"attackDetected"`
			} `json:"userPromptAnalysis"`
			DocumentsAnalysis []struct {
				AttackDetected bool `json:"attackDetected"`
			} `json:"documentsAnalysis"`
		}
		url := strings.TrimRight(r.URL, "/") + "/contentsafety/text:shieldPrompt?api-version=" + orDefault(r.Version, "2024-09-01")
		err = e.vendorCall(ctx, r, url, map[string]any{"userPrompt": truncate(latestUser, 10000), "documents": docs}, map[string]string{"Ocp-Apim-Subscription-Key": key}, &out)
		if out.UserPromptAnalysis.AttackDetected {
			flagged, why = true, "user prompt attack"
		}
		for _, d := range out.DocumentsAnalysis {
			if d.AttackDetected {
				flagged, why = true, "indirect attack in a document or tool result"
			}
		}
	case "azure_content_safety":
		th := r.Max
		if th == 0 {
			th = 4
		}
		url := strings.TrimRight(r.URL, "/") + "/contentsafety/text:analyze?api-version=" + orDefault(r.Version, "2024-09-01")
		subject := latestUser
		if in.Phase == "output" {
			subject = strings.Join(users, "\n")
		}
		for _, chunk := range chunks(subject, 10000) {
			var out struct {
				CategoriesAnalysis []struct {
					Category string `json:"category"`
					Severity int    `json:"severity"`
				} `json:"categoriesAnalysis"`
			}
			if err = e.vendorCall(ctx, r, url, map[string]any{"text": chunk, "outputType": "FourSeverityLevels"}, map[string]string{"Ocp-Apim-Subscription-Key": key}, &out); err != nil {
				break
			}
			for _, c := range out.CategoriesAnalysis {
				if c.Severity >= th {
					flagged, why = true, fmt.Sprintf("%s severity %d", strings.ToLower(c.Category), c.Severity)
				}
			}
			if flagged {
				break
			}
		}
	case "openai_moderation":
		url := orDefault(r.URL, "https://api.openai.com/v1/moderations")
		subject := []string{latestUser}
		if in.Phase == "output" {
			subject = users
		}
		var out struct {
			Results []struct {
				Flagged    bool            `json:"flagged"`
				Categories map[string]bool `json:"categories"`
			} `json:"results"`
		}
		err = e.vendorCall(ctx, r, url, map[string]any{"model": orDefault(r.Model, "omni-moderation-latest"), "input": subject}, map[string]string{"Authorization": "Bearer " + key}, &out)
		for _, x := range out.Results {
			if x.Flagged {
				flagged = true
				for c, v := range x.Categories {
					if v {
						why = c
						break
					}
				}
			}
		}
	}
	if err != nil {
		if r.FailOpen != nil && !*r.FailOpen {
			block(r.Type + " unavailable")
			return
		}
		res.add(Finding{Policy: p.Name, Rule: r.Type, Action: "error", Detail: truncate(err.Error(), 200), Phase: in.Phase})
		return
	}
	if flagged {
		msg := r.Message
		if msg == "" {
			msg = "flagged by " + r.Type
			if why != "" {
				msg += " (" + why + ")"
			}
		}
		block(msg)
	}
}

func chunks(s string, n int) []string {
	if s == "" {
		return nil
	}
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	return append(out, s)
}
