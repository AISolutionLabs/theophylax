package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"theophylax/internal/auth"
	"theophylax/internal/metrics"
	"theophylax/internal/providers"
	"theophylax/internal/secrets"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

// Message batches implement Anthropic's Message Batches API
// (/v1/messages/batches) on top of the gateway itself: each request in a
// batch runs later through the same pipeline as a live /v1/messages call —
// identity, model access, rate limits and budgets, guardrails, Secret Shield,
// routing and failover, logging, cost and receipts — on whatever provider
// the model routes to, including Bedrock, Vertex, OpenAI-format and local
// models. Batch requests yield to live traffic: they wait out rate limits
// and provider overload instead of failing, until the batch expires.
//
// Parameters and results are stored encrypted and deleted once the batch is
// deleted or its results expire.

const (
	batchMaxRequests = 10000
	batchExpiry      = 24 * time.Hour
	batchRetention   = 29 * 24 * time.Hour
	batchStaleAfter  = 15 * time.Minute // a claimed request with no outcome is retried
	batchMaxRetries  = 3                // for upstream 5xx; rate limits are retried until expiry
)

var customIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

type principalCtxKey struct{}

// withPrincipal marks an internal request as already authenticated. The key
// type is unexported, so a client request can never carry it.
func withPrincipal(ctx context.Context, p *auth.Principal) context.Context {
	return context.WithValue(ctx, principalCtxKey{}, p)
}

// Batches serves /v1/messages/batches and runs queued batch requests.
type Batches struct {
	G       *Gateway
	Box     *secrets.Box
	Workers int
}

func (b *Batches) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/messages/batches", b.create)
	mux.HandleFunc("GET /v1/messages/batches", b.list)
	mux.HandleFunc("GET /v1/messages/batches/{id}", b.get)
	mux.HandleFunc("GET /v1/messages/batches/{id}/results", b.results)
	mux.HandleFunc("POST /v1/messages/batches/{id}/cancel", b.cancel)
	mux.HandleFunc("DELETE /v1/messages/batches/{id}", b.delete)
}

type batchRow struct {
	ID                string     `db:"id"`
	OrgID             string     `db:"org_id"`
	TeamID            *string    `db:"team_id"`
	ProjectID         string     `db:"project_id"`
	KeyID             *string    `db:"key_id"`
	UserID            *string    `db:"user_id"`
	Via               string     `db:"via"`
	ClientIP          string     `db:"client_ip"`
	Status            string     `db:"status"`
	Total             int        `db:"total"`
	CreatedAt         time.Time  `db:"created_at"`
	ExpiresAt         time.Time  `db:"expires_at"`
	CancelInitiatedAt *time.Time `db:"cancel_initiated_at"`
	EndedAt           *time.Time `db:"ended_at"`
}

type batchCounts struct {
	Processing int `json:"processing"`
	Succeeded  int `json:"succeeded"`
	Errored    int `json:"errored"`
	Canceled   int `json:"canceled"`
	Expired    int `json:"expired"`
}

func (b *Batches) view(ctx context.Context, r *batchRow) (map[string]any, error) {
	var c batchCounts
	rows, err := b.G.St.Pool.Query(ctx, `SELECT status, count(*) FROM message_batch_items WHERE batch_id=$1 GROUP BY status`, r.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			rows.Close()
			return nil, err
		}
		switch s {
		case "pending", "running":
			c.Processing += n
		case "succeeded":
			c.Succeeded = n
		case "errored":
			c.Errored = n
		case "canceled":
			c.Canceled = n
		case "expired":
			c.Expired = n
		}
	}
	rows.Close()
	ts := func(t *time.Time) any {
		if t == nil {
			return nil
		}
		return t.UTC().Format(time.RFC3339Nano)
	}
	var results any
	if r.Status == "ended" {
		results = b.G.Cfg.PublicURL + "/v1/messages/batches/" + r.ID + "/results"
	}
	return map[string]any{
		"id": r.ID, "type": "message_batch", "processing_status": r.Status, "request_counts": c,
		"ended_at": ts(r.EndedAt), "created_at": ts(&r.CreatedAt), "expires_at": ts(&r.ExpiresAt),
		"archived_at": nil, "cancel_initiated_at": ts(r.CancelInitiatedAt), "results_url": results,
	}, nil
}

// auth authenticates a batch API call; batches belong to a project.
func (b *Batches) auth(w http.ResponseWriter, r *http.Request) (*auth.Principal, bool) {
	p, err := b.G.authenticate(r)
	if err != nil {
		writeErr(w, providers.FormatAnthropic, err)
		return nil, false
	}
	if p.ProjectID == "" {
		writeErr(w, providers.FormatAnthropic, util.Err(403, "permission_error", "message batches belong to a project; use a project key"))
		return nil, false
	}
	return p, true
}

func (b *Batches) load(w http.ResponseWriter, r *http.Request, p *auth.Principal) (*batchRow, bool) {
	row, err := store.Get[batchRow](r.Context(), b.G.St.Pool, `SELECT * FROM message_batches WHERE id=$1 AND project_id=$2`, r.PathValue("id"), p.ProjectID)
	if err != nil {
		if errors.Is(err, util.ErrNotFound) {
			err = util.Err(404, "not_found_error", "message batch not found")
		}
		writeErr(w, providers.FormatAnthropic, err)
		return nil, false
	}
	return row, true
}

func (b *Batches) create(w http.ResponseWriter, r *http.Request) {
	p, ok := b.auth(w, r)
	if !ok {
		return
	}
	bad := func(msg string) { writeErr(w, providers.FormatAnthropic, util.Err(400, "invalid_request_error", msg)) }
	max := b.G.Cfg.BatchMaxBytes
	if max <= 0 {
		max = 64 << 20
	}
	var in struct {
		Requests []struct {
			CustomID string          `json:"custom_id"`
			Params   json.RawMessage `json:"params"`
		} `json:"requests"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, max)).Decode(&in); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeErr(w, providers.FormatAnthropic, util.Err(413, "request_too_large", fmt.Sprintf("a batch may be at most %d MB", max>>20)))
			return
		}
		bad("invalid JSON body: " + err.Error())
		return
	}
	if len(in.Requests) == 0 {
		bad("requests: at least one request is required")
		return
	}
	if len(in.Requests) > batchMaxRequests {
		bad(fmt.Sprintf("requests: a batch may contain at most %d requests", batchMaxRequests))
		return
	}
	seen := make(map[string]bool, len(in.Requests))
	for i, q := range in.Requests {
		if !customIDRe.MatchString(q.CustomID) {
			bad(fmt.Sprintf("requests.%d.custom_id: 1-64 characters of letters, digits, _ and -", i))
			return
		}
		if seen[q.CustomID] {
			bad(fmt.Sprintf("requests.%d.custom_id: %q is used more than once", i, q.CustomID))
			return
		}
		seen[q.CustomID] = true
		var params map[string]any
		if err := json.Unmarshal(q.Params, &params); err != nil || params == nil {
			bad(fmt.Sprintf("requests.%d.params: must be a Messages API request object", i))
			return
		}
		if m, _ := params["model"].(string); m == "" {
			bad(fmt.Sprintf("requests.%d.params.model: required", i))
			return
		}
		if s, _ := params["stream"].(bool); s {
			bad(fmt.Sprintf("requests.%d.params.stream: streaming is not supported in batches", i))
			return
		}
	}
	now := time.Now().UTC()
	row := &batchRow{ID: util.NewID("msgbatch"), OrgID: p.OrgID, TeamID: strPtr(p.TeamID), ProjectID: p.ProjectID, Via: p.Via,
		ClientIP: util.ClientIP(r, b.G.Cfg.TrustedProxies), Status: "in_progress", Total: len(in.Requests), CreatedAt: now, ExpiresAt: now.Add(batchExpiry)}
	if p.Key != nil {
		row.KeyID = &p.Key.ID
	} else if uid := p.UserID(); uid != "" {
		row.UserID = &uid
	} else {
		writeErr(w, providers.FormatAnthropic, util.Err(403, "permission_error", "this credential cannot create batches"))
		return
	}
	err := b.G.St.Tx(r.Context(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `INSERT INTO message_batches (id,org_id,team_id,project_id,key_id,user_id,via,client_ip,status,total,created_at,expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, row.ID, row.OrgID, row.TeamID, row.ProjectID, row.KeyID, row.UserID, row.Via, row.ClientIP, row.Status, row.Total, row.CreatedAt, row.ExpiresAt); err != nil {
			return err
		}
		_, err := tx.CopyFrom(r.Context(), pgx.Identifier{"message_batch_items"}, []string{"batch_id", "idx", "custom_id", "params_enc"},
			pgx.CopyFromSlice(len(in.Requests), func(i int) ([]any, error) {
				return []any{row.ID, i, in.Requests[i].CustomID, b.Box.Seal(in.Requests[i].Params)}, nil
			}))
		return err
	})
	if err != nil {
		writeErr(w, providers.FormatAnthropic, err)
		return
	}
	metrics.BatchRequests.Add(float64(len(in.Requests)), "queued")
	v, err := b.view(r.Context(), row)
	if err != nil {
		writeErr(w, providers.FormatAnthropic, err)
		return
	}
	util.WriteJSON(w, http.StatusOK, v)
}

func (b *Batches) list(w http.ResponseWriter, r *http.Request) {
	p, ok := b.auth(w, r)
	if !ok {
		return
	}
	limit := 20
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 1000 {
			writeErr(w, providers.FormatAnthropic, util.Err(400, "invalid_request_error", "limit must be between 1 and 1000"))
			return
		}
		limit = n
	}
	q := `SELECT * FROM message_batches WHERE project_id=$1`
	args := []any{p.ProjectID}
	order := "DESC"
	if id := r.URL.Query().Get("after_id"); id != "" { // older than id
		q += ` AND (created_at, id) < (SELECT created_at, id FROM message_batches WHERE id=$2)`
		args = append(args, id)
	} else if id := r.URL.Query().Get("before_id"); id != "" { // newer than id
		q += ` AND (created_at, id) > (SELECT created_at, id FROM message_batches WHERE id=$2)`
		args = append(args, id)
		order = "ASC"
	}
	list, err := store.List[batchRow](r.Context(), b.G.St.Pool, fmt.Sprintf(`%s ORDER BY created_at %s, id %s LIMIT %d`, q, order, order, limit+1), args...)
	if err != nil {
		writeErr(w, providers.FormatAnthropic, err)
		return
	}
	more := len(list) > limit
	if more {
		list = list[:limit]
	}
	if order == "ASC" {
		for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
			list[i], list[j] = list[j], list[i]
		}
	}
	data := make([]any, 0, len(list))
	for i := range list {
		v, err := b.view(r.Context(), &list[i])
		if err != nil {
			writeErr(w, providers.FormatAnthropic, err)
			return
		}
		data = append(data, v)
	}
	out := map[string]any{"data": data, "has_more": more, "first_id": nil, "last_id": nil}
	if len(list) > 0 {
		out["first_id"], out["last_id"] = list[0].ID, list[len(list)-1].ID
	}
	util.WriteJSON(w, http.StatusOK, out)
}

func (b *Batches) get(w http.ResponseWriter, r *http.Request) {
	p, ok := b.auth(w, r)
	if !ok {
		return
	}
	row, ok := b.load(w, r, p)
	if !ok {
		return
	}
	v, err := b.view(r.Context(), row)
	if err != nil {
		writeErr(w, providers.FormatAnthropic, err)
		return
	}
	util.WriteJSON(w, http.StatusOK, v)
}

func (b *Batches) results(w http.ResponseWriter, r *http.Request) {
	p, ok := b.auth(w, r)
	if !ok {
		return
	}
	row, ok := b.load(w, r, p)
	if !ok {
		return
	}
	if row.Status != "ended" {
		writeErr(w, providers.FormatAnthropic, util.Err(400, "invalid_request_error", "results are available once the batch has ended"))
		return
	}
	rows, err := b.G.St.Pool.Query(r.Context(), `SELECT custom_id, status, result_enc FROM message_batch_items WHERE batch_id=$1 ORDER BY idx`, row.ID)
	if err != nil {
		writeErr(w, providers.FormatAnthropic, err)
		return
	}
	defer rows.Close()
	w.Header().Set("Content-Type", "application/x-jsonl")
	enc := json.NewEncoder(w)
	for rows.Next() {
		var cid, status string
		var res []byte
		if err := rows.Scan(&cid, &status, &res); err != nil {
			return
		}
		result := map[string]any{"type": status}
		if len(res) > 0 {
			plain, err := b.Box.Open(res)
			if err != nil {
				slog.Error("batch result decrypt", "batch", row.ID, "err", err)
				continue
			}
			switch status {
			case "succeeded":
				result["message"] = json.RawMessage(plain)
			case "errored":
				result["error"] = json.RawMessage(plain)
			}
		}
		_ = enc.Encode(map[string]any{"custom_id": cid, "result": result})
	}
}

func (b *Batches) cancel(w http.ResponseWriter, r *http.Request) {
	p, ok := b.auth(w, r)
	if !ok {
		return
	}
	row, ok := b.load(w, r, p)
	if !ok {
		return
	}
	if row.Status == "in_progress" {
		if err := b.G.St.Pool.QueryRow(r.Context(), `UPDATE message_batches SET status='canceling', cancel_initiated_at=now() WHERE id=$1 AND status='in_progress'
			RETURNING status, cancel_initiated_at`, row.ID).Scan(&row.Status, &row.CancelInitiatedAt); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, providers.FormatAnthropic, err)
			return
		}
		b.cancelPending(r.Context())
	}
	v, err := b.view(r.Context(), row)
	if err != nil {
		writeErr(w, providers.FormatAnthropic, err)
		return
	}
	util.WriteJSON(w, http.StatusOK, v)
}

func (b *Batches) delete(w http.ResponseWriter, r *http.Request) {
	p, ok := b.auth(w, r)
	if !ok {
		return
	}
	row, ok := b.load(w, r, p)
	if !ok {
		return
	}
	if row.Status != "ended" {
		writeErr(w, providers.FormatAnthropic, util.Err(400, "invalid_request_error", "a batch can be deleted once it has ended; cancel it first"))
		return
	}
	if _, err := b.G.St.Pool.Exec(r.Context(), `DELETE FROM message_batches WHERE id=$1`, row.ID); err != nil {
		writeErr(w, providers.FormatAnthropic, err)
		return
	}
	util.WriteJSON(w, http.StatusOK, map[string]any{"id": row.ID, "type": "message_batch_deleted"})
}

// ---------------------------------------------------------------------------
// Processing

// Run processes queued batch requests until ctx ends. Every replica may run
// it: requests are claimed with SKIP LOCKED, so each runs once.
func (b *Batches) Run(ctx context.Context) {
	if b.Workers <= 0 {
		return
	}
	sem := make(chan struct{}, b.Workers)
	var wg sync.WaitGroup
	t := time.NewTicker(time.Second)
	defer t.Stop()
	lastMaint := time.Time{}
	for {
		if time.Since(lastMaint) > 5*time.Second {
			b.maintain(ctx)
			lastMaint = time.Now()
		}
		if free := b.Workers - len(sem); free > 0 {
			items, err := b.claim(ctx, free)
			if err != nil && ctx.Err() == nil {
				slog.Warn("batch claim", "err", err)
			}
			for _, it := range items {
				sem <- struct{}{}
				wg.Add(1)
				go func(it claimed) {
					defer func() { <-sem; wg.Done() }()
					b.process(ctx, it)
				}(it)
			}
			if len(items) == free {
				continue // more may be waiting
			}
		}
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-t.C:
		}
	}
}

type claimed struct {
	batchID  string
	idx      int
	params   []byte
	attempts int
}

func (b *Batches) claim(ctx context.Context, n int) ([]claimed, error) {
	rows, err := b.G.St.Pool.Query(ctx, `UPDATE message_batch_items i SET status='running', claimed_at=now(), attempts=i.attempts+1
		FROM (SELECT batch_id, idx FROM message_batch_items WHERE status='pending' AND next_at <= now()
		      ORDER BY next_at, batch_id, idx LIMIT $1 FOR UPDATE SKIP LOCKED) c
		WHERE i.batch_id=c.batch_id AND i.idx=c.idx
		RETURNING i.batch_id, i.idx, i.params_enc, i.attempts`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []claimed
	for rows.Next() {
		var c claimed
		if err := rows.Scan(&c.batchID, &c.idx, &c.params, &c.attempts); err != nil {
			return out, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// maintain cancels, expires, recovers and ends batches, and deletes batches
// whose results have expired.
func (b *Batches) maintain(ctx context.Context) {
	db := b.G.St.Pool
	b.cancelPending(ctx)
	if _, err := db.Exec(ctx, `UPDATE message_batch_items SET status='expired', params_enc=NULL
		WHERE status='pending' AND batch_id IN (SELECT id FROM message_batches WHERE status <> 'ended' AND expires_at < now())`); err != nil && ctx.Err() == nil {
		slog.Warn("batch maintenance", "err", err)
		return
	}
	_, _ = db.Exec(ctx, `UPDATE message_batch_items SET status='pending', next_at=now() WHERE status='running' AND claimed_at < $1`, time.Now().Add(-batchStaleAfter))
	_, _ = db.Exec(ctx, `UPDATE message_batches mb SET status='ended', ended_at=now() WHERE status <> 'ended'
		AND NOT EXISTS (SELECT 1 FROM message_batch_items WHERE batch_id=mb.id AND status IN ('pending','running'))`)
	_, _ = db.Exec(ctx, `DELETE FROM message_batches WHERE status='ended' AND ended_at < $1`, time.Now().Add(-batchRetention))
}

func (b *Batches) cancelPending(ctx context.Context) {
	_, _ = b.G.St.Pool.Exec(ctx, `UPDATE message_batch_items SET status='canceled', params_enc=NULL
		WHERE status='pending' AND batch_id IN (SELECT id FROM message_batches WHERE status='canceling')`)
	_, _ = b.G.St.Pool.Exec(ctx, `UPDATE message_batches mb SET status='ended', ended_at=now() WHERE status='canceling'
		AND NOT EXISTS (SELECT 1 FROM message_batch_items WHERE batch_id=mb.id AND status IN ('pending','running'))`)
}

// principal re-authenticates the batch's creator for each request, so a
// revoked key, a suspended user or a lost project membership stops the batch.
func (b *Batches) principal(ctx context.Context, row *batchRow) (*auth.Principal, error) {
	if row.KeyID != nil {
		k, err := store.GetByID[store.APIKey](ctx, b.G.St.Pool, "api_keys", *row.KeyID)
		if err != nil {
			return nil, util.Err(401, "authentication_error", "the API key that created this batch no longer exists")
		}
		return b.G.Resolver.FromKeyHash(ctx, k.Hash, "gateway")
	}
	p, err := b.G.Resolver.UserPrincipal(ctx, util.Deref(row.UserID), row.Via)
	if err != nil {
		return nil, err
	}
	proj := b.G.Loader.Snapshot().Projects[row.ProjectID]
	if proj == nil || !p.Can(auth.Scope{Type: "project", ID: proj.ID, OrgID: proj.OrgID, TeamID: proj.TeamID}, "member") {
		return nil, util.Err(403, "permission_error", "the batch's creator is no longer a member of its project")
	}
	p.OrgID, p.TeamID, p.ProjectID = proj.OrgID, proj.TeamID, proj.ID
	return p, nil
}

func (b *Batches) process(ctx context.Context, it claimed) {
	db := b.G.St.Pool
	row, err := store.Get[batchRow](ctx, db, `SELECT * FROM message_batches WHERE id=$1`, it.batchID)
	if err != nil {
		return // deleted meanwhile
	}
	settle := func(status string, result []byte, reqID string) {
		var enc []byte
		if result != nil {
			enc = b.Box.Seal(result)
		}
		if _, err := db.Exec(ctx, `UPDATE message_batch_items SET status=$3, result_enc=$4, request_id=NULLIF($5,''), params_enc=NULL, claimed_at=NULL
			WHERE batch_id=$1 AND idx=$2`, it.batchID, it.idx, status, enc, reqID); err != nil && ctx.Err() == nil {
			slog.Warn("batch settle", "batch", it.batchID, "err", err)
		}
		metrics.BatchRequests.Inc(status)
	}
	retry := func(after time.Duration) {
		_, _ = db.Exec(ctx, `UPDATE message_batch_items SET status='pending', claimed_at=NULL, next_at=now()+$3::interval WHERE batch_id=$1 AND idx=$2`,
			it.batchID, it.idx, fmt.Sprintf("%d milliseconds", after.Milliseconds()))
	}
	if row.Status == "canceling" {
		settle("canceled", nil, "")
		return
	}
	if time.Now().After(row.ExpiresAt) {
		settle("expired", nil, "")
		return
	}
	params, err := b.Box.Open(it.params)
	if err != nil {
		settle("errored", errBody(util.Err(500, "api_error", "stored request could not be read")), "")
		return
	}
	p, err := b.principal(ctx, row)
	if err != nil {
		var he *util.HTTPError
		if !errors.As(err, &he) {
			retry(30 * time.Second) // database trouble: try again
			return
		}
		settle("errored", errBody(he), "")
		return
	}
	req, err := http.NewRequestWithContext(withPrincipal(ctx, p), http.MethodPost, "/v1/messages/batches", bytes.NewReader(params))
	if err != nil {
		settle("errored", errBody(util.Err(500, "api_error", err.Error())), "")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "theophylax-batch")
	req.RemoteAddr = row.ClientIP
	rec := &batchRecorder{h: http.Header{}, code: 200}
	b.G.inference(rec, req, providers.FormatAnthropic, "")
	if ctx.Err() != nil {
		return // shutting down: the stale-claim sweep requeues it
	}
	reqID := rec.h.Get("X-Request-Id")
	switch {
	case rec.code == 200:
		settle("succeeded", rec.buf.Bytes(), reqID)
	case rec.code == 429:
		// Rate limits and exhausted budgets: wait for capacity until expiry.
		retry(min(time.Duration(it.attempts)*30*time.Second, 5*time.Minute))
	case rec.code >= 500 && it.attempts < batchMaxRetries:
		retry(time.Duration(it.attempts) * 20 * time.Second)
	default:
		settle("errored", rec.buf.Bytes(), reqID)
	}
}

func errBody(he *util.HTTPError) []byte {
	b, _ := json.Marshal(map[string]any{"type": "error", "error": he})
	return b
}

// batchRecorder captures an internal response.
type batchRecorder struct {
	h     http.Header
	code  int
	buf   bytes.Buffer
	wrote bool
}

func (r *batchRecorder) Header() http.Header { return r.h }
func (r *batchRecorder) WriteHeader(c int) {
	if !r.wrote {
		r.code, r.wrote = c, true
	}
}
func (r *batchRecorder) Write(p []byte) (int, error) {
	r.WriteHeader(200)
	if r.buf.Len() > 32<<20 {
		return 0, io.ErrShortWrite
	}
	return r.buf.Write(p)
}
func (r *batchRecorder) Flush() {}
