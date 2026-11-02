package db

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigrationsRunOnceAndKeepData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "users.db")

	d, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO users (id, status, created_at, updated_at) VALUES ('u1', 'pending', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	d.Close()

	d, err = Open(ctx, path) // the second start applies nothing and loses nothing
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var users, migrations int
	d.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users)
	d.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrations)
	if users != 1 || migrations != 3 {
		t.Fatalf("users=%d migrations=%d, want 1 and 3", users, migrations)
	}
}

func TestForeignKeysAreEnforced(t *testing.T) {
	d, err := Open(context.Background(), filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	_, err = d.Exec(`INSERT INTO identities (user_id, channel, external_id, last_seen_at, created_at) VALUES ('nobody', 'telegram', '1', 1, 1)`)
	if err == nil {
		t.Fatal("an identity without a user was accepted")
	}
}
