package gateway

import (
	"context"
	"os"
	"testing"
	"time"

	"theophylax/internal/db"
	"theophylax/internal/store"
)

func TestRateLimiterLocal(t *testing.T) {
	rl := NewRateLimiter()
	allowed := 0
	for i := 0; i < 50; i++ {
		if ok, _, _ := rl.Allow("k", 10, 0); ok {
			allowed++
		}
	}
	if allowed != 10 {
		t.Fatalf("allowed %d, want 10", allowed)
	}
	rl.AddTokens("t", 500)
	if ok, _, why := rl.Allow("t", 0, 500); ok || why != "tokens per minute" {
		t.Fatalf("token limit not enforced: %v %q", ok, why)
	}
}

// TestRateLimiterGlobal runs two replicas against one database and checks the
// fleet-wide total stays at the limit (plus at most one sync interval of slack).
func TestRateLimiterGlobal(t *testing.T) {
	dsn := os.Getenv("THEOPHYLAX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("THEOPHYLAX_TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool, err := db.Connect(ctx, dsn, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	key := "test:" + time.Now().Format("150405.000000")
	st := store.New(pool)
	a := NewSharedRateLimiter(st, 50*time.Millisecond)
	b := NewSharedRateLimiter(st, 50*time.Millisecond)
	go a.Run(ctx)
	go b.Run(ctx)

	// Wait for the start of a fresh minute window if we're near the boundary.
	if s := time.Now().Second(); s > 50 {
		time.Sleep(time.Duration(61-s) * time.Second)
	}
	total := 0
	for i := 0; i < 60; i++ {
		for _, rl := range []*RateLimiter{a, b} {
			if ok, _, _ := rl.Allow(key, 20, 0); ok {
				total++
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Unsynchronised replicas would admit 40. Allowed slack is what two
	// replicas can admit during one sync interval at this request rate.
	if total < 20 || total > 30 {
		t.Fatalf("fleet admitted %d requests against a limit of 20", total)
	}
	t.Logf("fleet admitted %d against a limit of 20 (per-replica limiting would admit 40)", total)
	time.Sleep(150 * time.Millisecond)
	var reqs int64
	if err := pool.QueryRow(ctx, `SELECT coalesce(sum(reqs),0) FROM rate_windows WHERE key=$1`, key).Scan(&reqs); err != nil {
		t.Fatal(err)
	}
	if int(reqs) != total {
		t.Fatalf("database has %d, replicas admitted %d", reqs, total)
	}
}

// TestAllowAllAtomic verifies that when a later scope's limit rejects a
// request, the earlier scopes are not charged for it.
func TestAllowAllAtomic(t *testing.T) {
	rl := NewRateLimiter()
	// org is exhausted; key and project have plenty of headroom.
	lim := []Limit{{Key: "key:k", RPM: 100}, {Key: "project:p", RPM: 100}, {Key: "org:o", RPM: 1}}
	if ok, _, _, _ := rl.AllowAll(lim); !ok {
		t.Fatal("first request should be allowed")
	}
	// org now at its limit of 1 -> next request rejected, attributed to org.
	ok, _, why, key := rl.AllowAll(lim)
	if ok || key != "org:o" || why != "requests per minute" {
		t.Fatalf("expected org rejection, got ok=%v why=%q key=%q", ok, why, key)
	}
	// The rejected request must not have been counted against key or project:
	// each should still have 1 (from the single allowed request), so 99 more
	// requests against them alone must all pass.
	for i := 0; i < 99; i++ {
		if ok, _, _, _ := rl.AllowAll([]Limit{{Key: "key:k", RPM: 100}, {Key: "project:p", RPM: 100}}); !ok {
			t.Fatalf("key/project over-counted: rejected at i=%d", i)
		}
	}
}
