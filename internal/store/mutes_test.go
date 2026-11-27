package store

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestAlertMutesAreKeptBoundedAndPruned(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	if err := s.SetAlertMute(ctx, AlertMute{Key: "down/1", Until: now.Add(time.Hour), CreatedBy: "alex"}); err != nil {
		t.Fatal(err)
	}
	// The same key again replaces its mute.
	if err := s.SetAlertMute(ctx, AlertMute{Key: "down/1", Until: now.Add(8 * time.Hour), Severity: "critical", CreatedBy: "sam"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAlertMute(ctx, AlertMute{Key: "disk//media", Until: now.Add(time.Minute), CreatedBy: "alex"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.AlertMutes(ctx)
	if err != nil || len(got) != 2 || got[1].Key != "down/1" || !got[1].Until.Equal(now.Add(8*time.Hour)) || got[1].CreatedBy != "sam" || got[1].Severity != "critical" {
		t.Fatalf("mutes = %+v, %v", got, err)
	}

	if err := s.PruneAlertMutes(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.AlertMutes(ctx); len(got) != 1 || got[0].Key != "down/1" {
		t.Fatalf("after pruning: %+v", got)
	}
	if err := s.DeleteAlertMutes(ctx, "down/1", "never/there"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.AlertMutes(ctx); len(got) != 0 {
		t.Fatalf("after deleting: %+v", got)
	}

	for i := range MaxAlertMutes {
		if err := s.SetAlertMute(ctx, AlertMute{Key: "k/" + strconv.Itoa(i), Until: now.Add(time.Hour), CreatedBy: "alex"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetAlertMute(ctx, AlertMute{Key: "one/more", Until: now.Add(time.Hour), CreatedBy: "alex"}); !errors.Is(err, ErrTooMany) {
		t.Fatalf("beyond the bound: %v", err)
	}
	// A key that is muted already can still be extended.
	if err := s.SetAlertMute(ctx, AlertMute{Key: "k/0", Until: now.Add(2 * time.Hour), CreatedBy: "alex"}); err != nil {
		t.Fatalf("extending at the bound: %v", err)
	}
}
