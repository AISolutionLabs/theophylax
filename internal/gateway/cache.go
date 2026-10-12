package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"theophylax/internal/auth"
	"theophylax/internal/secrets"
	"theophylax/internal/store"
)

// ResponseCache stores complete upstream responses so identical requests can
// be answered without calling the provider again. It works at the upstream
// layer: a cached response is replayed through the normal pipeline, so format
// translation, output guardrails, Secret Shield restoration, usage accounting
// and receipts behave exactly as for a live response.
//
// Entries are isolated per organization and project, encrypted at rest, and
// only written for requests that completed successfully. The cache is off
// unless a scope sets cache_ttl_seconds.
type ResponseCache struct {
	St  *store.Store
	Box *secrets.Box
}

// maxCacheEntry bounds a cached response body.
const maxCacheEntry = 4 << 20

// CacheHeader lets a client opt out per request: "off" neither reads nor
// writes the cache, "refresh" skips the lookup but stores the new response.
const CacheHeader = "X-Theophylax-Cache"

// cacheTTL resolves the scope's cache setting: the most specific scope that
// sets cache_ttl_seconds wins, and -1 turns caching off.
func cacheTTL(snap *Snapshot, p *auth.Principal) time.Duration {
	for _, s := range []*ScopeInfo{snap.Projects[p.ProjectID], snap.Teams[p.TeamID], snap.Orgs[p.OrgID]} {
		if s != nil && s.Settings.CacheTTL != 0 {
			if s.Settings.CacheTTL < 0 {
				return 0
			}
			return time.Duration(s.Settings.CacheTTL) * time.Second
		}
	}
	return 0
}

// cacheKey identifies an upstream request exactly: tenancy (so entries are
// never shared across organizations or projects), the deployment, the
// upstream model, streaming, the request body as sent upstream (after
// guardrail redaction and agent overlays) and the headers that change output.
func cacheKey(p *auth.Principal, providerID, upstream string, stream bool, body map[string]any, hdr http.Header) []byte {
	b, _ := json.Marshal(body) // map keys are sorted, so this is canonical
	var hs []string
	for _, k := range []string{"anthropic-version", "anthropic-beta"} {
		vs := append([]string{}, hdr.Values(k)...)
		sort.Strings(vs)
		hs = append(hs, k+"="+strings.Join(vs, ","))
	}
	parts, _ := json.Marshal([]any{"v1", p.OrgID, p.ProjectID, p.AgentID, providerID, upstream, stream, hs})
	h := sha256.New()
	h.Write(parts)
	h.Write([]byte{0})
	h.Write(b)
	return h.Sum(nil)
}

// Get returns a replayable response for key, or nil on a miss.
func (c *ResponseCache) Get(ctx context.Context, key []byte) *http.Response {
	var ct string
	var enc []byte
	if err := c.St.Pool.QueryRow(ctx, `SELECT content_type, body_enc FROM response_cache WHERE key=$1 AND expires_at > now()`, key).Scan(&ct, &enc); err != nil {
		return nil
	}
	body, err := c.Box.Open(enc)
	if err != nil {
		return nil
	}
	h := http.Header{}
	if ct != "" {
		h.Set("Content-Type", ct)
	}
	return &http.Response{StatusCode: 200, Status: "200 OK", Header: h, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body))}
}

// Put stores a complete response body.
func (c *ResponseCache) Put(ctx context.Context, key []byte, orgID, projectID, contentType string, body []byte, ttl time.Duration) {
	if _, err := c.St.Pool.Exec(ctx, `INSERT INTO response_cache (key, org_id, project_id, expires_at, content_type, body_enc, size) VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (key) DO UPDATE SET created_at=now(), expires_at=EXCLUDED.expires_at, content_type=EXCLUDED.content_type, body_enc=EXCLUDED.body_enc, size=EXCLUDED.size`,
		key, orgID, projectID, time.Now().Add(ttl), contentType, c.Box.Seal(body), len(body)); err != nil {
		slog.Warn("response cache: store failed", "err", err)
	}
}

// Purge removes an organization's entries (or one project's) and reports how many.
func (c *ResponseCache) Purge(ctx context.Context, orgID, projectID string) (int64, error) {
	q, args := `DELETE FROM response_cache WHERE org_id=$1`, []any{orgID}
	if projectID != "" {
		q, args = q+` AND project_id=$2`, append(args, projectID)
	}
	tag, err := c.St.Pool.Exec(ctx, q, args...)
	return tag.RowsAffected(), err
}

// Run deletes expired entries every few minutes.
func (c *ResponseCache) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = c.St.Pool.Exec(ctx, `DELETE FROM response_cache WHERE expires_at < now()`)
		}
	}
}

// captureBody records an upstream body as it is read so it can be cached once
// the request has completed successfully.
type captureBody struct {
	io.ReadCloser
	buf      bytes.Buffer
	overflow bool
	eof      bool
}

func (b *captureBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 && !b.overflow {
		if b.buf.Len()+n > maxCacheEntry {
			b.overflow = true
			b.buf = bytes.Buffer{}
		} else {
			b.buf.Write(p[:n])
		}
	}
	if err == io.EOF {
		b.eof = true
	}
	return n, err
}

// complete reports whether the whole body was read and fits in the cache.
func (b *captureBody) complete() bool { return b != nil && b.eof && !b.overflow }
