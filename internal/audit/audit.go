// Package audit writes a tamper-evident, hash-chained audit trail of every
// control-plane mutation and security-relevant event.
package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"theophylax/internal/store"
	"theophylax/internal/util"
)

type Actor struct {
	Type  string // user | key | system | scim
	ID    string
	Email string
	IP    string
	UA    string
}

type Event struct {
	Actor      Actor
	OrgID      string
	Action     string // e.g. team.create, key.revoke, auth.login
	TargetType string
	TargetID   string
	Details    any
}

type Logger struct {
	st *store.Store
}

func New(st *store.Store) *Logger { return &Logger{st: st} }

func canonical(e *store.AuditEntry) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s|%s|%s|%s|%s|%s|%s|%s|%s|", e.ID, e.TS.UTC().Format(time.RFC3339Nano), e.ActorType, e.ActorID, e.ActorEmail, util.Deref(e.OrgID), e.Action, e.TargetType, e.TargetID)
	b.Write(e.Details)
	fmt.Fprintf(&b, "|%s|%s|", e.IP, e.UserAgent)
	b.Write(e.PrevHash)
	return b.Bytes()
}

// Record appends an event. Serialized via advisory lock so the chain is linear.
func (l *Logger) Record(ctx context.Context, ev Event) {
	if err := l.record(ctx, ev); err != nil {
		slog.Error("audit write failed", "action", ev.Action, "err", err)
	}
}

func (l *Logger) record(ctx context.Context, ev Event) error {
	details, _ := json.Marshal(ev.Details)
	if ev.Details == nil {
		details = []byte("{}")
	}
	e := &store.AuditEntry{
		ID: util.NewID("aud"), TS: time.Now().UTC().Truncate(time.Microsecond),
		ActorType: ev.Actor.Type, ActorID: ev.Actor.ID, ActorEmail: ev.Actor.Email,
		Action: ev.Action, TargetType: ev.TargetType, TargetID: ev.TargetID,
		Details: details, IP: ev.Actor.IP, UserAgent: ev.Actor.UA,
	}
	if ev.OrgID != "" {
		e.OrgID = &ev.OrgID
	}
	return l.st.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(919191)`); err != nil {
			return err
		}
		var prev []byte
		err := tx.QueryRow(ctx, `SELECT hash FROM audit_log ORDER BY seq DESC LIMIT 1`).Scan(&prev)
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		e.PrevHash = prev
		// Round-trip details through jsonb normalisation so verification is stable.
		var norm []byte
		if err := tx.QueryRow(ctx, `SELECT $1::jsonb::text`, string(details)).Scan(&norm); err != nil {
			return err
		}
		e.Details = norm
		h := sha256.Sum256(canonical(e))
		e.Hash = h[:]
		_, err = tx.Exec(ctx, `INSERT INTO audit_log (id, ts, actor_type, actor_id, actor_email, org_id, action, target_type, target_id, details, ip, user_agent, prev_hash, hash)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,$12,$13,$14)`,
			e.ID, e.TS, e.ActorType, e.ActorID, e.ActorEmail, e.OrgID, e.Action, e.TargetType, e.TargetID, string(e.Details), e.IP, e.UserAgent, e.PrevHash, e.Hash)
		return err
	})
}

type VerifyResult struct {
	OK       bool   `json:"ok"`
	Checked  int64  `json:"checked"`
	BrokenAt *int64 `json:"broken_at,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// Verify walks the full chain and recomputes every hash.
func (l *Logger) Verify(ctx context.Context) (*VerifyResult, error) {
	rows, err := l.st.Pool.Query(ctx, `SELECT seq,id,ts,actor_type,actor_id,actor_email,org_id,action,target_type,target_id,details::text,ip,user_agent,prev_hash,hash FROM audit_log ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := &VerifyResult{OK: true}
	var prev []byte
	for rows.Next() {
		var e store.AuditEntry
		var details string
		if err := rows.Scan(&e.Seq, &e.ID, &e.TS, &e.ActorType, &e.ActorID, &e.ActorEmail, &e.OrgID, &e.Action, &e.TargetType, &e.TargetID, &details, &e.IP, &e.UserAgent, &e.PrevHash, &e.Hash); err != nil {
			return nil, err
		}
		e.Details = []byte(details)
		res.Checked++
		if !bytes.Equal(e.PrevHash, prev) {
			res.OK, res.BrokenAt, res.Reason = false, &e.Seq, "prev_hash does not match preceding entry (row deleted or reordered)"
			return res, nil
		}
		h := sha256.Sum256(canonical(&e))
		if !bytes.Equal(h[:], e.Hash) {
			res.OK, res.BrokenAt, res.Reason = false, &e.Seq, "hash mismatch (row modified)"
			return res, nil
		}
		prev = e.Hash
	}
	return res, rows.Err()
}
