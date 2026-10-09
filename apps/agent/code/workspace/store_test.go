package workspace

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testStore(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	owner := "owner-" + NewID()
	base := time.Now().UTC().Truncate(time.Millisecond)
	a := Workspace{ID: NewID(), Owner: owner, Spec: Spec{RepoURL: "https://github.com/a/b", BaseBranch: "main"},
		Image: DefaultImage, Dir: "/tmp/a", State: StateStopped, CreatedAt: base, LastUsedAt: base}
	b := a
	b.ID, b.CreatedAt = NewID(), base.Add(time.Second)
	for _, ws := range []Workspace{a, b} {
		if err := s.Put(ctx, ws); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { s.Delete(ctx, a.ID); s.Delete(ctx, b.ID) })

	a.State, a.LastUsedAt = StateRunning, base.Add(time.Hour)
	if err := s.Put(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateRunning || !got.LastUsedAt.Equal(a.LastUsedAt) || got.RepoURL != a.RepoURL || got.Dir != "/tmp/a" {
		t.Fatalf("Get = %+v", got)
	}
	list, err := s.List(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != b.ID {
		t.Fatalf("List = %+v", list)
	}
	if err := s.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get deleted err = %v", err)
	}
}

func TestMemoryStore(t *testing.T) { testStore(t, NewMemoryStore()) }

func TestPostgresStore(t *testing.T) {
	conn := os.Getenv("POSTGRES_CONN_STRING")
	if conn == "" {
		t.Skip("POSTGRES_CONN_STRING is not set")
	}
	pool, err := pgxpool.New(context.Background(), conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s, err := NewPostgresStore(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	testStore(t, s)
}
