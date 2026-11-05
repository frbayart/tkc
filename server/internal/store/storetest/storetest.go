// Package storetest provides a migrated, throw-away PostgreSQL schema for
// integration tests. Tests are skipped unless TKC_TEST_DATABASE_URL is set.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/frbayart/tkc/server/internal/store"
)

// EnvVar names the environment variable holding the test database URL.
const EnvVar = "TKC_TEST_DATABASE_URL"

// Pool returns a pool bound to a fresh schema with all migrations applied.
// The schema is dropped when the test ends.
func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv(EnvVar)
	if url == "" {
		t.Skip(EnvVar + " not set")
	}
	ctx := context.Background()

	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "test_" + hex.EncodeToString(b)

	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := store.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}
