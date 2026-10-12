package alerts

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDeliveryFormats(t *testing.T) {
	var got struct {
		body []byte
		hdr  http.Header
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.body, _ = io.ReadAll(r.Body)
		got.hdr = r.Header.Clone()
	}))
	defer srv.Close()
	d := &Deliverer{HTTP: srv.Client()}
	e := Event{Type: BudgetThreshold, OrgID: "org_1", Title: "Budget at 80%", Text: "The monthly budget…", Link: "https://gw.example/budgets", TS: time.Now()}

	if err := d.Send(context.Background(), Target{Type: "slack", URL: srv.URL}, e); err != nil {
		t.Fatal(err)
	}
	var slack map[string]string
	if json.Unmarshal(got.body, &slack) != nil || !strings.Contains(slack["text"], "*Budget at 80%*") || !strings.Contains(slack["text"], "<https://gw.example/budgets|") {
		t.Fatalf("slack payload: %s", got.body)
	}

	if err := d.Send(context.Background(), Target{Type: "teams", URL: srv.URL}, e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got.body), "application/vnd.microsoft.card.adaptive") || !strings.Contains(string(got.body), "Action.OpenUrl") {
		t.Fatalf("teams payload: %s", got.body)
	}

	// Webhooks are signed over "<timestamp>.<body>" when a secret is set.
	if err := d.Send(context.Background(), Target{Type: "webhook", URL: srv.URL, Secret: "s3cret"}, e); err != nil {
		t.Fatal(err)
	}
	m := hmac.New(sha256.New, []byte("s3cret"))
	m.Write([]byte(got.hdr.Get("X-Theophylax-Timestamp") + "."))
	m.Write(got.body)
	if got.hdr.Get("X-Theophylax-Signature") != "sha256="+hex.EncodeToString(m.Sum(nil)) || got.hdr.Get("X-Theophylax-Event") != BudgetThreshold {
		t.Fatalf("webhook signature/headers: %v", got.hdr)
	}
	var ev Event
	if json.Unmarshal(got.body, &ev) != nil || ev.Type != BudgetThreshold || ev.OrgID != "org_1" {
		t.Fatalf("webhook body: %s", got.body)
	}

	if err := d.Send(context.Background(), Target{Type: "email", To: []string{"a@example.com"}}, e); err == nil {
		t.Fatal("email without SMTP config should fail")
	}
}

func TestDeliveryRetriesThenFails(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
	defer srv.Close()
	err := (&Deliverer{HTTP: srv.Client()}).Send(context.Background(), Target{Type: "slack", URL: srv.URL}, Event{Title: "x"})
	if err == nil || calls != 2 {
		t.Fatalf("want one retry then error, got calls=%d err=%v", calls, err)
	}
}

func TestParseSMTPURL(t *testing.T) {
	c, err := ParseSMTPURL("smtp://user:p%40ss@mail.example.com?from=alerts@example.com")
	if err != nil || c.Addr != "mail.example.com:587" || c.Pass != "p@ss" || c.Implicit || c.From != "alerts@example.com" {
		t.Fatalf("starttls: %+v %v", c, err)
	}
	c, err = ParseSMTPURL("smtps://mail.example.com?from=alerts@example.com")
	if err != nil || c.Addr != "mail.example.com:465" || !c.Implicit {
		t.Fatalf("implicit tls: %+v %v", c, err)
	}
	if _, err := ParseSMTPURL("smtp://mail.example.com"); err == nil {
		t.Fatal("missing from accepted")
	}
	if c, err := ParseSMTPURL(""); c != nil || err != nil {
		t.Fatal("empty URL should mean email disabled")
	}
}
