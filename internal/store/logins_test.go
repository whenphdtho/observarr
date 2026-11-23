package store

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func TestKnownLogins(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	t0 := time.Unix(1_800_000_000, 0)
	// addr0 … addr1003, a second apart, in one statement: one RememberLogin each would make this test slow.
	if _, err := s.db.Exec(`INSERT INTO login_known (key, at) WITH RECURSIVE n(i) AS
		(SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i < ?) SELECT 'addr' || i, ? + i FROM n`,
		maxKnownLogins+3, t0.Unix()); err != nil {
		t.Fatal(err)
	}
	last := maxKnownLogins + 4
	if err := s.RememberLogin(ctx, "addr"+strconv.Itoa(last), t0.Add(time.Duration(last)*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.RememberLogin(ctx, "addr10", t0.Add(time.Hour)); err != nil { // a later login moves it forward
		t.Fatal(err)
	}
	known, err := s.KnownLogins(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(known) != maxKnownLogins {
		t.Fatalf("%d known addresses, want %d", len(known), maxKnownLogins)
	}
	if _, ok := known["addr4"]; ok {
		t.Fatal("the oldest logins must be dropped beyond the limit")
	}
	if !known["addr10"].Equal(t0.Add(time.Hour)) {
		t.Fatalf("addr10 at %v, want its latest login", known["addr10"])
	}

	since := t0.Add(500 * time.Second)
	const recent = maxKnownLogins + 5 - 500 + 1 // addr500 and later, and addr10
	if known, _ := s.KnownLogins(ctx, since); len(known) != recent {
		t.Fatalf("%d logins since %v, want %d", len(known), since, recent)
	}
	if err := s.PruneKnownLogins(ctx, since); err != nil {
		t.Fatal(err)
	}
	if known, _ := s.KnownLogins(ctx, time.Time{}); len(known) != recent {
		t.Fatalf("%d logins after pruning, want %d", len(known), recent)
	}
}
