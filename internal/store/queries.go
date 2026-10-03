package store

import (
	"context"
	"strings"
)

func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	return Get[User](ctx, s.Pool, `SELECT * FROM users WHERE lower(email)=lower($1)`, strings.TrimSpace(email))
}

func (s *Store) UserByID(ctx context.Context, id string) (*User, error) {
	return GetByID[User](ctx, s.Pool, "users", id)
}

func (s *Store) MembershipsOf(ctx context.Context, userID string) ([]Membership, error) {
	return List[Membership](ctx, s.Pool, `SELECT * FROM memberships WHERE user_id=$1`, userID)
}

func (s *Store) KeyByHash(ctx context.Context, hash []byte) (*APIKey, error) {
	return Get[APIKey](ctx, s.Pool, `SELECT * FROM api_keys WHERE hash=$1`, hash)
}
