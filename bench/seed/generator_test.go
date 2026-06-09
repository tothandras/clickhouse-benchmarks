package seed

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
)

// goldenEventStreamSHA256 is the digest of the first 100k events (seed 42,
// TimeEnd pinned to 2026-06-01T00:00:00Z) computed with the pre-Generator
// seeder code. The Generator refactor must keep the stream byte-identical.
const goldenEventStreamSHA256 = "9b7f2af70f7b04c8be6aa10e7e8fbd036a83c97e04211a3a569f919df96ee2d5"

func goldenConfig() Config {
	cfg := DefaultConfig()
	cfg.TimeEnd = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	return cfg
}

func hashEvents(gen *Generator, n int) string {
	h := sha256.New()
	for range n {
		e := gen.Next()
		fmt.Fprintf(h, "%s|%s|%s|%s|%d|%s|%s|%d\n",
			e.ID, e.Namespace, e.Type, e.Subject, e.Time.UnixNano(), e.Data, e.StoreRowID, e.StoredAt.UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestGeneratorGoldenDigest(t *testing.T) {
	gen, err := NewGenerator(goldenConfig())
	if err != nil {
		t.Fatal(err)
	}
	if got := hashEvents(gen, 100_000); got != goldenEventStreamSHA256 {
		t.Fatalf("event stream diverged from pre-refactor seeder:\n got  %s\n want %s", got, goldenEventStreamSHA256)
	}
}

func TestGeneratorMatchesIndexedAccess(t *testing.T) {
	cfg := goldenConfig()
	gen, err := NewGenerator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2_000 {
		streamed := gen.Next()
		if at := gen.At(i); at != streamed {
			t.Fatalf("index %d: Next() and At() diverged:\n %+v\n %+v", i, streamed, at)
		}
	}
	if gen.Index() != 2_000 {
		t.Fatalf("cursor at %d, want 2000", gen.Index())
	}
}

func TestGeneratorTimeOverride(t *testing.T) {
	cfg := goldenConfig()
	base, err := NewGenerator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	live, err := NewGenerator(cfg)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	for i := range 5_000 {
		want := base.Next()
		stamp := now.Add(time.Duration(i) * time.Millisecond)
		got := live.NextAt(stamp)

		// Everything except the time-derived fields must be byte-identical.
		if got.ID != want.ID || got.Namespace != want.Namespace || got.Type != want.Type ||
			got.Subject != want.Subject || got.Data != want.Data {
			t.Fatalf("index %d: payload stream diverged under time override:\n %+v\n %+v", i, got, want)
		}
		if !got.Time.Equal(stamp) || !got.StoredAt.Equal(stamp) {
			t.Fatalf("index %d: time not stamped: time=%v stored_at=%v want %v", i, got.Time, got.StoredAt, stamp)
		}
		// store_row_id keeps its time-ordering property: the ULID timestamp
		// prefix encodes the overridden time, entropy comes from the unchanged
		// stream (same suffix as the seeder's ULID).
		id, err := ulid.Parse(got.StoreRowID)
		if err != nil {
			t.Fatalf("index %d: store_row_id not a ULID: %v", i, err)
		}
		if id.Time() != ulid.Timestamp(stamp) {
			t.Fatalf("index %d: store_row_id timestamp %d, want %d", i, id.Time(), ulid.Timestamp(stamp))
		}
		wantID := ulid.MustParse(want.StoreRowID)
		if !bytes.Equal(id.Entropy(), wantID.Entropy()) {
			t.Fatalf("index %d: store_row_id entropy diverged under time override", i)
		}
	}
}

func TestGeneratorSeekPartition(t *testing.T) {
	// Two workers over disjoint halves must reproduce the sequential stream
	// exactly — the parallel-preload determinism contract.
	cfg := goldenConfig()
	full, err := NewGenerator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w1, _ := NewGenerator(cfg)
	w2, _ := NewGenerator(cfg)
	w2.Seek(1000)
	for i := range 2000 {
		want := full.Next()
		var got Event
		if i < 1000 {
			got = w1.Next()
		} else {
			got = w2.Next()
		}
		if got != want {
			t.Fatalf("index %d: partitioned stream diverged from sequential", i)
		}
	}
}

func TestNewGeneratorValidates(t *testing.T) {
	cfg := goldenConfig()
	cfg.TimeSpan = 0
	if _, err := NewGenerator(cfg); err == nil {
		t.Fatal("TimeSpan=0 must be rejected (the time draw needs a positive span)")
	}
	cfg = goldenConfig()
	cfg.EventTypes = nil
	if _, err := NewGenerator(cfg); err == nil {
		t.Fatal("empty EventTypes must be rejected")
	}
}
