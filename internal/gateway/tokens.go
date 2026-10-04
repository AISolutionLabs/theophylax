package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"theophylax/internal/providers"
	"theophylax/internal/secrets"
	"theophylax/internal/store"
)

// tokenStore persists custom-provider tokens in provider_tokens, encrypted
// with the master key. Updates hold a transaction-scoped advisory lock per
// provider so only one replica refreshes at a time (refresh tokens are often
// single-use).
type tokenStore struct {
	st  *store.Store
	box *secrets.Box
}

func (t *tokenStore) Load(ctx context.Context, id string) (*providers.Tokens, error) {
	return t.load(ctx, t.st.Pool, id)
}

func (t *tokenStore) load(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id string) (*providers.Tokens, error) {
	var seed string
	var acc, ref []byte
	var exp *time.Time
	err := q.QueryRow(ctx, `SELECT seed, access_enc, refresh_enc, expires_at FROM provider_tokens WHERE provider_id=$1`, id).Scan(&seed, &acc, &ref, &exp)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := &providers.Tokens{Seed: seed}
	if out.Access, err = t.box.OpenString(acc); err != nil {
		return nil, err
	}
	if len(ref) > 0 {
		if out.Refresh, err = t.box.OpenString(ref); err != nil {
			return nil, err
		}
	}
	if exp != nil {
		out.Expiry = *exp
	}
	return out, nil
}

func (t *tokenStore) Update(ctx context.Context, id string, fn func(cur *providers.Tokens) (*providers.Tokens, error)) (*providers.Tokens, error) {
	tx, err := t.st.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('provider_tokens:' || $1))`, id); err != nil {
		return nil, err
	}
	cur, err := t.load(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	next, err := fn(cur)
	if err != nil {
		return nil, err
	}
	if next != cur {
		var ref []byte
		if next.Refresh != "" {
			ref = t.box.SealString(next.Refresh)
		}
		var exp *time.Time
		if !next.Expiry.IsZero() {
			exp = &next.Expiry
		}
		if _, err := tx.Exec(ctx, `INSERT INTO provider_tokens (provider_id, seed, access_enc, refresh_enc, expires_at, updated_at) VALUES ($1,$2,$3,$4,$5,now())
			ON CONFLICT (provider_id) DO UPDATE SET seed=EXCLUDED.seed, access_enc=EXCLUDED.access_enc, refresh_enc=EXCLUDED.refresh_enc, expires_at=EXCLUDED.expires_at, updated_at=now()`,
			id, next.Seed, t.box.SealString(next.Access), ref, exp); err != nil {
			return nil, err
		}
	}
	return next, tx.Commit(ctx)
}
