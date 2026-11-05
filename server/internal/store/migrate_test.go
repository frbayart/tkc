package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrationsEmbedded(t *testing.T) {
	migs, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migs) == 0 || migs[0].Version != 1 {
		t.Fatalf("unexpected migrations: %+v", migs)
	}
	for i, m := range migs {
		if m.Version != i+1 {
			t.Fatalf("migration versions must be contiguous, got %d at index %d", m.Version, i)
		}
	}
}

// testPool returns a pool bound to a fresh, empty schema, dropped at cleanup.
// Integration tests are skipped unless TKC_TEST_DATABASE_URL is set.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TKC_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TKC_TEST_DATABASE_URL not set")
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
	return pool
}

func migrated(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testPool(t)
	if _, err := Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestMigrateIdempotent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	first, err := Migrate(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 {
		t.Fatal("no migration applied on empty schema")
	}
	second, err := Migrate(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("re-applied migrations %v", second)
	}
}

func TestMigrateConcurrent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make([][]int, 4)
	errs := make([]error, 4)
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = Migrate(ctx, pool)
		}()
	}
	wg.Wait()
	total := 0
	for i := range 4 {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		total += len(results[i])
	}
	migs, _ := Migrations()
	if total != len(migs) {
		t.Fatalf("applied %d migrations in total, want %d", total, len(migs))
	}
}

// fixture inserts two families, each with a parent and a child, and returns
// their IDs ordered so that childA < childB.
type fixture struct {
	familyA, familyB, parentA, parentB, childA, childB string
}

func newFixture(t *testing.T, pool *pgxpool.Pool) fixture {
	t.Helper()
	ctx := context.Background()
	var f fixture
	mk := func(family string, role string) string {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO users (family_id, role) VALUES ($1, $2) RETURNING id`, family, role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	for _, fam := range []*string{&f.familyA, &f.familyB} {
		if err := pool.QueryRow(ctx, `INSERT INTO families DEFAULT VALUES RETURNING id`).Scan(fam); err != nil {
			t.Fatal(err)
		}
	}
	f.parentA, f.parentB = mk(f.familyA, "parent"), mk(f.familyB, "parent")
	f.childA, f.childB = mk(f.familyA, "child"), mk(f.familyB, "child")
	if f.childA > f.childB {
		f.childA, f.childB = f.childB, f.childA
		f.familyA, f.familyB = f.familyB, f.familyA
		f.parentA, f.parentB = f.parentB, f.parentA
	}
	return f
}

// wantViolation asserts err is a PostgreSQL constraint violation.
func wantViolation(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code[:2] != "23" {
		t.Fatalf("want integrity constraint violation, got %v", err)
	}
}

func TestSchemaConstraints(t *testing.T) {
	pool := migrated(t)
	ctx := context.Background()
	f := newFixture(t, pool)

	insertRel := func(childA, familyA, childB, familyB, status string, approved bool) error {
		var at any
		if approved {
			at = time.Now()
		}
		_, err := pool.Exec(ctx, `INSERT INTO relationships
			(child_a, family_a, child_b, family_b, status, origin, approved_a_at, approved_b_at)
			VALUES ($1, $2, $3, $4, $5, 'in_person', $6, $6)`,
			childA, familyA, childB, familyB, status, at)
		return err
	}

	t.Run("invalid role", func(t *testing.T) {
		_, err := pool.Exec(ctx, `INSERT INTO users (family_id, role) VALUES ($1, 'admin')`, f.familyA)
		wantViolation(t, err)
	})
	t.Run("relationship requires canonical order", func(t *testing.T) {
		wantViolation(t, insertRel(f.childB, f.familyB, f.childA, f.familyA, "proposed", false))
	})
	t.Run("relationship child must belong to stated family", func(t *testing.T) {
		wantViolation(t, insertRel(f.childA, f.familyB, f.childB, f.familyB, "proposed", false))
	})
	t.Run("active requires both approvals", func(t *testing.T) {
		wantViolation(t, insertRel(f.childA, f.familyA, f.childB, f.familyB, "active", false))
	})
	t.Run("one live relationship per pair", func(t *testing.T) {
		if err := insertRel(f.childA, f.familyA, f.childB, f.familyB, "active", true); err != nil {
			t.Fatal(err)
		}
		wantViolation(t, insertRel(f.childA, f.familyA, f.childB, f.familyB, "proposed", false))
	})
	t.Run("child exchange invitation requires a child of the issuer family", func(t *testing.T) {
		ins := func(child any) error {
			_, err := pool.Exec(ctx, `INSERT INTO invitations
				(kind, issuer_family_id, issuer_user_id, child_id, token_hash, expires_at)
				VALUES ('child_exchange', $1, $2, $3, gen_random_uuid()::text::bytea, now() + interval '5 minutes')`,
				f.familyA, f.parentA, child)
			return err
		}
		wantViolation(t, ins(nil))
		wantViolation(t, ins(f.childB)) // other family's child
		if err := ins(f.childA); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("revoked device needs revoked_at", func(t *testing.T) {
		_, err := pool.Exec(ctx, `INSERT INTO devices
			(user_id, device_type, platform, identity_public_key, encryption_public_key, status)
			VALUES ($1, 'phone', 'android', 'idk', 'enk', 'revoked')`, f.childA)
		wantViolation(t, err)
	})
	t.Run("ended call needs termination reason", func(t *testing.T) {
		var relID string
		if err := pool.QueryRow(ctx, `SELECT id FROM relationships LIMIT 1`).Scan(&relID); err != nil {
			t.Fatal(err)
		}
		_, err := pool.Exec(ctx, `INSERT INTO calls
			(relationship_id, caller_user_id, call_type, authorized_until, ended_at)
			VALUES ($1, $2, 'audio', now() + interval '20 minutes', now())`, relID, f.childA)
		wantViolation(t, err)
	})
	t.Run("family connection canonical order", func(t *testing.T) {
		lo, hi := f.familyA, f.familyB
		if lo > hi {
			lo, hi = hi, lo
		}
		_, err := pool.Exec(ctx, `INSERT INTO family_connections (family_a, family_b, status) VALUES ($1, $2, 'pending')`, hi, lo)
		wantViolation(t, err)
		if _, err := pool.Exec(ctx, `INSERT INTO family_connections (family_a, family_b, status) VALUES ($1, $2, 'pending')`, lo, hi); err != nil {
			t.Fatal(err)
		}
	})
}
