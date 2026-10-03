package gateway

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"theophylax/internal/audit"
	"theophylax/internal/receipts"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

// ReceiptRoutes exposes receipt retrieval, public verification and the JWKS.
func (g *Gateway) ReceiptRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/theophylax-receipt-keys.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		util.WriteJSON(w, 200, map[string]any{"keys": []any{g.Signer.JWK()}})
	})
	mux.HandleFunc("POST /v1/receipts/verify", g.verifyReceipt)
	mux.HandleFunc("GET /v1/receipts/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, err := g.authenticate(r)
		if err != nil {
			util.WriteError(w, err)
			return
		}
		rec, err := LookupReceipt(r.Context(), g.St, g.Signer, r.PathValue("id"))
		if err != nil {
			util.WriteError(w, err)
			return
		}
		rc := rec["receipt"].(*receipts.Receipt)
		sameKey := p.Key != nil && rc.Principal.Type == "key" && rc.Principal.ID == p.Key.ID
		if rc.OrgID != p.OrgID || !(sameKey || rc.ProjectID == p.ProjectID) {
			util.WriteError(w, util.ErrNotFound)
			return
		}
		util.WriteJSON(w, 200, rec)
	})
}

// LookupReceipt loads, verifies and checks Merkle inclusion for a receipt.
func LookupReceipt(ctx context.Context, st *store.Store, signer *receipts.Signer, id string) (map[string]any, error) {
	var jws, kid string
	var ckpt *string
	var leaf []byte
	err := st.Pool.QueryRow(ctx, `SELECT jws, kid, checkpoint_id, hash FROM receipts WHERE request_id=$1`, id).Scan(&jws, &kid, &ckpt, &leaf)
	if err != nil {
		return nil, util.Err(404, "not_found_error", "receipt not found (receipts are persisted within ~1s of the response)")
	}
	rc, verr := receipts.Verify(jws, map[string]ed25519.PublicKey{signer.KID: signer.Pub})
	out := map[string]any{"jws": jws, "kid": kid, "signature_valid": verr == nil}
	if verr != nil {
		out["error"] = verr.Error()
		rc, _ = receipts.Decode(jws) // best-effort decode for display
	}
	out["receipt"] = rc
	if ckpt == nil {
		out["anchored"] = false
		out["anchor_status"] = "pending (anchored into the audit chain every few minutes)"
		return out, nil
	}
	var root []byte
	var cts time.Time
	var auditID *string
	var count int
	if err := st.Pool.QueryRow(ctx, `SELECT merkle_root, ts, audit_id, count FROM receipt_checkpoints WHERE id=$1`, *ckpt).Scan(&root, &cts, &auditID, &count); err != nil {
		return nil, err
	}
	rows, err := st.Pool.Query(ctx, `SELECT hash FROM receipts WHERE checkpoint_id=$1 ORDER BY ts, request_id`, *ckpt)
	if err != nil {
		return nil, err
	}
	var leaves [][]byte
	for rows.Next() {
		var h []byte
		_ = rows.Scan(&h)
		leaves = append(leaves, h)
	}
	rows.Close()
	recomputed := receipts.MerkleRoot(leaves)
	out["anchored"] = hex.EncodeToString(recomputed) == hex.EncodeToString(root) && len(leaves) == count
	out["checkpoint"] = map[string]any{"id": *ckpt, "merkle_root": hex.EncodeToString(root), "ts": cts, "audit_id": auditID, "count": count}
	return out, nil
}

func (g *Gateway) verifyReceipt(w http.ResponseWriter, r *http.Request) {
	var in struct {
		JWS      string `json:"jws"`
		Request  string `json:"request"`
		Response string `json:"response"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)).Decode(&in); err != nil {
		util.WriteError(w, util.Err(400, "invalid_request_error", "invalid JSON"))
		return
	}
	rc, err := receipts.Verify(in.JWS, map[string]ed25519.PublicKey{g.Signer.KID: g.Signer.Pub})
	out := map[string]any{"signature_valid": err == nil}
	if err != nil {
		out["error"] = err.Error()
		util.WriteJSON(w, 200, out)
		return
	}
	out["receipt"] = rc
	if in.Request != "" {
		out["request_matches"] = receipts.SHA256Hex([]byte(in.Request)) == rc.ReqSHA256
	}
	if in.Response != "" {
		out["response_matches"] = receipts.SHA256Hex([]byte(in.Response)) == rc.RespSHA256
	}
	util.WriteJSON(w, 200, out)
}

// RunCheckpoints periodically anchors new receipts as a Merkle root in the
// hash-chained audit log. Only one replica does this at a time.
func RunCheckpoints(ctx context.Context, st *store.Store, al *audit.Logger, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := checkpointOnce(ctx, st, al); err != nil && ctx.Err() == nil {
			slog.Warn("receipt checkpoint failed", "err", err)
		}
	}
}

func checkpointOnce(ctx context.Context, st *store.Store, al *audit.Logger) error {
	conn, err := st.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(424242)`).Scan(&got); err != nil || !got {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(424242)`)
	rows, err := conn.Query(ctx, `SELECT request_id, ts, hash FROM receipts WHERE checkpoint_id IS NULL AND ts < now() - interval '5 seconds' ORDER BY ts, request_id LIMIT 50000`)
	if err != nil {
		return err
	}
	var ids []string
	var leaves [][]byte
	var first, last time.Time
	for rows.Next() {
		var id string
		var ts time.Time
		var h []byte
		if err := rows.Scan(&id, &ts, &h); err != nil {
			rows.Close()
			return err
		}
		if len(ids) == 0 {
			first = ts
		}
		last = ts
		ids = append(ids, id)
		leaves = append(leaves, h)
	}
	rows.Close()
	if len(ids) == 0 {
		return nil
	}
	root := receipts.MerkleRoot(leaves)
	cid := util.NewID("ckp")
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO receipt_checkpoints (id, first_ts, last_ts, count, merkle_root) VALUES ($1,$2,$3,$4,$5)`, cid, first, last, len(ids), root); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE receipts SET checkpoint_id=$1 WHERE request_id = ANY($2)`, cid, ids); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	al.Record(ctx, audit.Event{Actor: audit.Actor{Type: "system", ID: "receipts"}, Action: "receipts.checkpoint", TargetType: "receipt_checkpoint", TargetID: cid,
		Details: map[string]any{"merkle_root": hex.EncodeToString(root), "count": len(ids), "first_ts": first, "last_ts": last}})
	var aid string
	if conn.QueryRow(ctx, `SELECT id FROM audit_log WHERE target_id=$1 ORDER BY seq DESC LIMIT 1`, cid).Scan(&aid) == nil {
		_, _ = conn.Exec(ctx, `UPDATE receipt_checkpoints SET audit_id=$2 WHERE id=$1`, cid, aid)
	}
	slog.Info("receipts anchored", "checkpoint", cid, "count", len(ids))
	return nil
}
