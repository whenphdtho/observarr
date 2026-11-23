package adapter

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"syscall"
	"testing"
)

func TestDescribe(t *testing.T) {
	inURL := func(err error) error { return &url.Error{Op: "Get", URL: "http://h/secret-path", Err: err} }
	for _, c := range []struct {
		err         error
		phrase      string
		unreachable bool
	}{
		{fmt.Errorf("adapter: /x: %w", ErrNotJSON), ErrNotJSON.Error(), false},
		{ErrUnauthorized, ErrUnauthorized.Error(), false},
		{inURL(context.DeadlineExceeded), "timed out", true},
		{inURL(&net.DNSError{Err: "no such host", Name: "h", IsNotFound: true}), "host not found", true},
		{inURL(&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}), "connection refused", true},
		{inURL(errRefusedAddress), "link-local and cloud metadata addresses are not allowed", true},
		{fmt.Errorf("adapter: /x: %w", StatusError(502)), "unexpected status 502", false},
		{fmt.Errorf("adapter: /x: %w", ErrTooLarge), ErrTooLarge.Error(), false},
		{inURL(ErrCrossHostRedirect), "redirected to a different host", true},
		{inURL(errors.New("body: secret")), "request failed", true},
		{errors.New("adapter: unexpected status 500 from /x"), "", false},
	} {
		phrase, unreachable := Describe(c.err)
		if phrase != c.phrase || unreachable != c.unreachable {
			t.Errorf("Describe(%v) = %q, %v; want %q, %v", c.err, phrase, unreachable, c.phrase, c.unreachable)
		}
	}
}

func TestWrongApp(t *testing.T) {
	long := strings.Repeat("x", 63) + "é" // the rune straddles byte 64
	for in, want := range map[string]string{
		"Radarr":           "Radarr answers at this address",
		" Jelly\x1bfin\n ": "Jellyfin answers at this address",
		long:               strings.Repeat("x", 63) + " answers at this address",
		"":                 ErrWrongApp.Error(),
		"\x00\u200b\t":     ErrWrongApp.Error(),
	} {
		err := WrongApp(in)
		if !errors.Is(err, ErrWrongApp) || err.Error() != want {
			t.Errorf("WrongApp(%q) = %v, want %q wrapping ErrWrongApp", in, err, want)
		}
		if phrase, unreachable := Describe(fmt.Errorf("x: %w", err)); phrase != want || unreachable {
			t.Errorf("Describe(WrongApp(%q)) = %q, %v", in, phrase, unreachable)
		}
	}
}
