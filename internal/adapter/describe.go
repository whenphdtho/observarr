package adapter

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/url"
	"syscall"
)

// Describe reduces err, from an adapter call, to a short fixed phrase that never holds a URL, a key or
// anything the app sent, except the clipped app name of a WrongApp error. For one of this package's own errors
// it is that error's message and unreachable
// is false. For a failure to get an answer at all, it is the cause ("timed out", "connection refused", …) and
// unreachable is true, so the caller can name the host it could not reach. That includes ErrCrossHostRedirect:
// the app was not reached where the user pointed, so a caller shows e.g. "cannot reach <host>: redirected to a
// different host". For any other error phrase is "".
func Describe(err error) (phrase string, unreachable bool) {
	var wrongApp wrongAppError
	if errors.As(err, &wrongApp) {
		return wrongApp.Error(), false
	}
	for _, known := range []error{ErrWrongApp, ErrUnauthorized, ErrUnsupported, ErrNotFound, ErrNotJSON, ErrTooLarge,
		ErrUnknownKind} {
		if errors.Is(err, known) {
			return known.Error(), false
		}
	}
	var (
		status  StatusError
		urlErr  *url.Error
		netErr  net.Error
		dnsErr  *net.DNSError
		certErr *tls.CertificateVerificationError
	)
	switch {
	case errors.As(err, &status):
		return status.Error(), false
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout():
		return "timed out", true
	case errors.As(err, &dnsErr):
		return "host not found", true
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused", true
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset", true
	case errors.As(err, &certErr):
		return "TLS certificate not trusted", true
	case errors.Is(err, errRefusedAddress):
		return "link-local and cloud metadata addresses are not allowed", true
	case errors.Is(err, ErrCrossHostRedirect):
		return "redirected to a different host", true
	case errors.As(err, &urlErr):
		return "request failed", true
	}
	return "", false
}
