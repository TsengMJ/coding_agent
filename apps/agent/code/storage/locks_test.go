package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

// testLocks checks a and b, two handles on the same lock space (two
// processes for Postgres).
func testLocks(t *testing.T, a, b ThreadLocks) {
	t.Helper()
	ctx := context.Background()
	thread := fmt.Sprintf("lock-%d", time.Now().UnixNano())
	unlock, err := a.TryLock(ctx, thread)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.TryLock(ctx, thread); !errors.Is(err, ErrLocked) {
		t.Fatalf("second holder err = %v, want ErrLocked", err)
	}
	if _, err := a.TryLock(ctx, thread); !errors.Is(err, ErrLocked) {
		t.Fatalf("same handle again err = %v, want ErrLocked", err)
	}
	other, err := b.TryLock(ctx, thread+"-other")
	if err != nil {
		t.Fatalf("another thread err = %v", err)
	}
	other()
	unlock()
	unlock()
	again, err := b.TryLock(ctx, thread)
	if err != nil {
		t.Fatalf("lock after unlock err = %v", err)
	}
	again()
}

func TestMemoryLocks(t *testing.T) {
	l := NewMemoryLocks()
	testLocks(t, l, l)
}

func TestPostgresLocks(t *testing.T) {
	conn := os.Getenv("POSTGRES_CONN_STRING")
	if conn == "" {
		t.Skip("POSTGRES_CONN_STRING not set")
	}
	ctx := context.Background()
	a, err := NewPostgresLocks(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	b, err := NewPostgresLocks(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	testLocks(t, a, b)
}

func TestPostgresLocksAreReleasedWhenTheProcessGoes(t *testing.T) {
	conn := os.Getenv("POSTGRES_CONN_STRING")
	if conn == "" {
		t.Skip("POSTGRES_CONN_STRING not set")
	}
	ctx := context.Background()
	gone, err := NewPostgresLocks(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	thread := fmt.Sprintf("crash-%d", time.Now().UnixNano())
	if _, err := gone.TryLock(ctx, thread); err != nil {
		t.Fatal(err)
	}
	gone.Close()
	l, err := NewPostgresLocks(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	unlock, err := l.TryLock(ctx, thread)
	if err != nil {
		t.Fatalf("lock after the holder closed err = %v", err)
	}
	unlock()
}
