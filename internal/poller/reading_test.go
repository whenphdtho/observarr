package poller

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/shape"
)

func TestReadingError(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{&url.Error{Op: "Get", URL: "http://app:8989/redirected/to/evil?x", Err: errors.New("stopped after 10 redirects")},
			"cannot reach app:8989: request failed"},
		{&url.Error{Op: "Get", URL: "http://app:8989/x", Err: context.DeadlineExceeded}, "cannot reach app:8989: timed out"},
		{fmt.Errorf("adapter: /api/v3/health: %w", adapter.StatusError(500)), "unexpected status 500"},
		{fmt.Errorf("adapter: /x: %w", adapter.ErrUnauthorized), adapter.ErrUnauthorized.Error()},
		{errCrashed, "adapter crashed"},
		{errors.New("arr: something with http://elsewhere/secret in it"), "the check failed"},
	} {
		if got := readingError("app:8989", c.err); got != c.want {
			t.Errorf("readingError(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func TestReadingNeverHoldsTheRequestURL(t *testing.T) {
	fa := newFakeAdapter("fake", shape.KindHealth)
	fa.probe = func(shape.Kind, int) (any, error) {
		return nil, &url.Error{Op: "Get", URL: "http://x/location-from-the-app", Err: errors.New("EOF")}
	}
	reg := adapter.NewRegistry()
	reg.Register(fa)
	hub := testHub(1, "fake", fa.info.Kinds)
	p := New(Config{Registry: reg, Hub: hub, Intervals: map[shape.Kind]time.Duration{shape.KindHealth: time.Hour}})
	defer p.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx, Service{ID: 1, Type: "fake", BaseURL: mustURL(t, "http://x")})

	eventually(t, time.Second, func() bool {
		r, ok := hub.Snapshot()[0].Readings[shape.KindHealth]
		return ok && r.Error != ""
	})
	if r := hub.Snapshot()[0].Readings[shape.KindHealth]; strings.Contains(r.Error, "location") || r.Error != "cannot reach x: request failed" {
		t.Fatalf("reading error = %q", r.Error)
	}
}
