// Package store is the Postgres persistence layer. It offers reflection-based
// CRUD helpers for entity structs (driven by `db` tags) plus targeted queries.
package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"theophylax/internal/util"
)

type Store struct {
	Pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{Pool: pool} }

var colCache sync.Map // reflect.Type -> []string

func columnsOf(t reflect.Type) []string {
	if v, ok := colCache.Load(t); ok {
		return v.([]string)
	}
	var cols []string
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("db")
		if tag == "" || tag == "-" {
			continue
		}
		cols = append(cols, tag)
	}
	colCache.Store(t, cols)
	return cols
}

func fieldValues(v any) ([]string, []any) {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	t := rv.Type()
	cols := columnsOf(t)
	vals := make([]any, 0, len(cols))
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("db")
		if tag == "" || tag == "-" {
			continue
		}
		f := rv.Field(i).Interface()
		// Ensure nil slices become empty arrays / json for NOT NULL columns.
		switch x := f.(type) {
		case []string:
			if x == nil {
				f = []string{}
			}
		}
		vals = append(vals, f)
	}
	return cols, vals
}

// Insert inserts the struct into table. Zero time.Time fields are left to DB defaults.
func (s *Store) Insert(ctx context.Context, q Querier, table string, v any) error {
	cols, vals := fieldValues(v)
	var useCols []string
	var useVals []any
	for i, c := range cols {
		if isZeroTime(vals[i]) || isNilJSON(c, vals[i]) {
			continue
		}
		useCols = append(useCols, c)
		useVals = append(useVals, vals[i])
	}
	ph := make([]string, len(useCols))
	for i := range ph {
		ph[i] = fmt.Sprintf("$%d", i+1)
	}
	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING %s", table, strings.Join(useCols, ","), strings.Join(ph, ","), strings.Join(cols, ","))
	rows, err := q.Query(ctx, sql, useVals...)
	if err != nil {
		return mapErr(err)
	}
	return mapErr(scanInto(rows, v))
}

// Update writes all columns except id/created_at for the row with v's ID.
func (s *Store) Update(ctx context.Context, q Querier, table string, v any) error {
	cols, vals := fieldValues(v)
	var sets []string
	var args []any
	var id any
	for i, c := range cols {
		switch c {
		case "id":
			id = vals[i]
			continue
		case "created_at", "seq":
			continue
		case "updated_at":
			sets = append(sets, "updated_at=now()")
			continue
		}
		if isNilJSON(c, vals[i]) {
			continue
		}
		args = append(args, vals[i])
		sets = append(sets, fmt.Sprintf("%s=$%d", c, len(args)))
	}
	args = append(args, id)
	sql := fmt.Sprintf("UPDATE %s SET %s WHERE id=$%d RETURNING %s", table, strings.Join(sets, ","), len(args), strings.Join(cols, ","))
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return mapErr(err)
	}
	return mapErr(scanInto(rows, v))
}

func (s *Store) Delete(ctx context.Context, q Querier, table, id string) error {
	tag, err := q.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id=$1", table), id)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return util.ErrNotFound
	}
	return nil
}

func scanInto(rows pgx.Rows, v any) error {
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return util.ErrNotFound
	}
	rv := reflect.ValueOf(v).Elem()
	t := rv.Type()
	fds := rows.FieldDescriptions()
	idx := map[string]int{}
	for i := 0; i < t.NumField(); i++ {
		idx[t.Field(i).Tag.Get("db")] = i
	}
	dst := make([]any, len(fds))
	for i, fd := range fds {
		fi, ok := idx[fd.Name]
		if !ok {
			var skip any
			dst[i] = &skip
			continue
		}
		dst[i] = rv.Field(fi).Addr().Interface()
	}
	if err := rows.Scan(dst...); err != nil {
		return err
	}
	rows.Close()
	return rows.Err()
}

func isZeroTime(v any) bool {
	t, ok := v.(time.Time)
	return ok && t.IsZero()
}

// isNilJSON skips nil json.RawMessage so the DB default applies.
func isNilJSON(col string, v any) bool {
	if b, ok := v.([]byte); ok && b == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() == reflect.Uint8 && rv.IsNil() {
		return true
	}
	return false
}

// Querier is satisfied by *pgxpool.Pool and pgx.Tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Get loads a single row by query into T.
func Get[T any](ctx context.Context, q Querier, sql string, args ...any) (*T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	v, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByNameLax[T])
	if err != nil {
		return nil, mapErr(err)
	}
	return v, nil
}

func GetByID[T any](ctx context.Context, q Querier, table, id string) (*T, error) {
	return Get[T](ctx, q, "SELECT * FROM "+table+" WHERE id=$1", id)
}

func List[T any](ctx context.Context, q Querier, sql string, args ...any) ([]T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[T])
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		out = []T{}
	}
	return out, nil
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return util.ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "23505":
			return util.Err(409, "conflict_error", "already exists: "+pe.ConstraintName)
		case "23503":
			return util.Err(400, "invalid_request_error", "referenced resource does not exist or is in use ("+pe.ConstraintName+")")
		case "23514":
			return util.Err(400, "invalid_request_error", "invalid value ("+pe.ConstraintName+")")
		case "22P02":
			return util.Err(400, "invalid_request_error", "invalid input syntax")
		}
	}
	return err
}

// Tx runs fn in a transaction.
func (s *Store) Tx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.Pool, fn)
}
