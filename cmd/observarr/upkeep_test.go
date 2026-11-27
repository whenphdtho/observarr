package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"observarr/internal/store"
)

// The first round of upkeep runs at once: a mute that has ended is gone, one that has not stays.
func TestUpkeepRemovesEndedMutes(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "o.db"), log)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithCancel(context.Background())
	for key, until := range map[string]time.Time{"down/1": time.Now().Add(-time.Minute), "down/2": time.Now().Add(time.Hour)} {
		if err := st.SetAlertMute(ctx, store.AlertMute{Key: key, Until: until, Severity: "critical", CreatedBy: "alex"}); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	go func() {
		upkeep(ctx, st, log)
		close(done)
	}()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		mutes, err := st.AlertMutes(context.Background())
		if err == nil && len(mutes) == 1 && mutes[0].Key == "down/2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("mutes after upkeep = %+v, %v", mutes, err)
		}
	}
	cancel()
	<-done
}
