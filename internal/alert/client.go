package alert

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"observarr/internal/adapter"
)

const (
	sendTimeout  = 15 * time.Second // one attempt to deliver one message, whatever the channel
	maxReplyRead = 512              // bytes of an answer read for its status line
	maxDrain     = 64 << 10         // bytes of an answer read at most, so the connection can be reused
	maxNote      = 160              // characters of a status line
)

// NewHTTPClient returns the client every channel sends with: bounded in time, refusing link-local and cloud
// metadata addresses at dial time (private addresses are fine: a self-hosted ntfy on the LAN is normal), with
// no proxy from the environment and no redirects followed at all, since a redirect would carry the message and
// its credentials somewhere the user did not enter.
func NewHTTPClient() *http.Client {
	return &http.Client{
		Timeout: sendTimeout,
		Transport: &http.Transport{
			DialContext:            adapter.Dialer(10 * time.Second).DialContext,
			ForceAttemptHTTP2:      true,
			MaxIdleConnsPerHost:    2,
			IdleConnTimeout:        90 * time.Second,
			TLSHandshakeTimeout:    10 * time.Second,
			ResponseHeaderTimeout:  10 * time.Second,
			MaxResponseHeaderBytes: 64 << 10,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// statusError is an answer other than 2xx. Reply is the start of its body, for the status line.
type statusError struct {
	code  int
	reply string
}

func (e *statusError) Error() string { return "HTTP " + strconv.Itoa(e.code) }

// permanent reports whether sending again cannot help: the destination refused the message itself.
func permanent(err error) bool {
	var se *statusError
	if errors.As(err, &se) {
		return se.code != http.StatusTooManyRequests && se.code < 500
	}
	var te *textproto.Error
	if errors.As(err, &te) {
		return te.Code >= 500
	}
	return adapter.IsRefusedAddress(err) || errors.Is(err, errNoStartTLS)
}

// post sends body to u with header and reads at most maxDrain bytes of the answer.
func post(ctx context.Context, c *http.Client, u string, header http.Header, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid address")
	}
	req.Header = header
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Observarr")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	reply, _ := io.ReadAll(io.LimitReader(resp.Body, maxReplyRead))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrain))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &statusError{code: resp.StatusCode, reply: string(reply)}
	}
	return nil
}

// describe is the short status line of a delivery: what went wrong in plain words, never a raw answer, an
// address with credentials or a secret.
func describe(err error, secrets []string) string {
	if err == nil {
		return "delivered"
	}
	var (
		se   *statusError
		te   *textproto.Error
		ue   *url.Error
		dns  *net.DNSError
		ca   x509.UnknownAuthorityError
		name x509.HostnameError
		cert x509.CertificateInvalidError
		ne   net.Error
	)
	var note string
	switch {
	case errors.As(err, &se) && se.code >= 300 && se.code < 400:
		note = se.Error() + ": the destination redirects elsewhere; redirects are not followed"
	case errors.As(err, &se):
		note = se.Error() + " " + http.StatusText(se.code)
		if line := firstLine(se.reply); line != "" {
			note += ": " + line
		}
	case errors.As(err, &te):
		note = "mail server: " + strconv.Itoa(te.Code) + " " + firstLine(te.Msg)
	case adapter.IsRefusedAddress(err):
		note = "refused: link-local and cloud metadata addresses are not allowed"
	case errors.As(err, &dns):
		note = "host not found"
	case errors.As(err, &ca), errors.As(err, &name), errors.As(err, &cert):
		note = "the TLS certificate is not trusted for this host"
	case errors.Is(err, syscall.ECONNREFUSED):
		note = "connection refused"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		note = "timed out"
	case errors.As(err, &ue):
		note = ue.Err.Error() // never the URL: it can hold a token
	default:
		note = err.Error()
	}
	return clean(note, secrets)
}

// firstLine is the first non-empty line of s.
func firstLine(s string) string {
	for line := range strings.Lines(s) {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// redacted lists what must never appear in a status line: every secret and, of a secret address, its path and
// query and their parts too, since an answer may echo just those. Longest first, so a whole is blanked before its
// parts.
func (sec Secrets) redacted() []string {
	var out []string
	for _, v := range sec {
		out = append(out, v)
		u, err := url.Parse(v)
		if err != nil || u.Host == "" {
			continue
		}
		out = append(out, u.EscapedPath(), u.Path, u.RawQuery)
		out = append(out, strings.Split(u.Path, "/")...)
		for _, vals := range u.Query() {
			out = append(out, vals...)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return len(b) - len(a) })
	return slices.DeleteFunc(out, func(s string) bool { return len(s) < 4 })
}

// clean makes text safe for a status line: secrets blanked, control characters gone, at most maxNote characters.
func clean(s string, secrets []string) string {
	for _, sec := range secrets {
		if len(sec) >= 4 {
			s = strings.ReplaceAll(s, sec, "…")
			s = strings.ReplaceAll(s, url.PathEscape(sec), "…")
			s = strings.ReplaceAll(s, url.QueryEscape(sec), "…")
		}
	}
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || !unicode.IsPrint(r) }), " ")
	if r := []rune(s); len(r) > maxNote {
		s = string(r[:maxNote-1]) + "…"
	}
	return s
}
