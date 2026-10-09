package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tsengmingchun/quill/apps/agent/migrate"
)

func testLog(t *testing.T, l Log) {
	t.Helper()
	ctx := context.Background()
	b := make([]byte, 4)
	rand.Read(b)
	user := "u-" + hex.EncodeToString(b)
	day := StartOfDay(time.Now())
	for _, ev := range []Event{
		{Time: day.Add(-time.Hour), User: user, Action: ActionRun, CostUSD: 5},
		{Time: day.Add(time.Minute), User: user, Action: ActionRun, ThreadID: "t1", CostUSD: 0.25},
		{Time: day.Add(2 * time.Minute), User: user, Action: ActionDecision, Detail: map[string]any{"types": []any{"approve"}}},
		{Time: day.Add(3 * time.Minute), User: user, Action: ActionRun, ThreadID: "t2", CostUSD: 0.5},
		{Time: day.Add(4 * time.Minute), User: user + "-other", Action: ActionRun, CostUSD: 9},
	} {
		if err := l.Record(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	spent, err := l.SpentSince(ctx, user, day)
	if err != nil || spent != 0.75 {
		t.Fatalf("spent = %v, %v", spent, err)
	}
	list, err := l.List(ctx, Filter{User: user, Since: day, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ThreadID != "t2" || list[1].Action != ActionDecision || list[1].Detail["types"] == nil {
		t.Fatalf("list = %+v", list)
	}
}

func TestMemoryLog(t *testing.T) { testLog(t, NewMemoryLog()) }

func TestPostgresLog(t *testing.T) {
	conn := os.Getenv("POSTGRES_CONN_STRING")
	if conn == "" {
		t.Skip("POSTGRES_CONN_STRING is not set")
	}
	pool, err := pgxpool.New(context.Background(), conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrate.Up(context.Background(), pool, migrate.Quill()); err != nil {
		t.Fatal(err)
	}
	l, err := NewPostgresLog(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	testLog(t, l)
}

func TestStartOfDayIsUTCMidnight(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	got := StartOfDay(time.Date(2026, 10, 9, 3, 0, 0, 0, loc))
	if want := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
