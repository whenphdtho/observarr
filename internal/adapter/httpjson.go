package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"syscall"
	"time"
)

// maxBodyBytes bounds the response body GetJSON will read, so a misbehaving or malicious app
// cannot exhaust memory.
const maxBodyBytes = 4 << 20 // 4 MiB

// maxHeaderBytes bounds the response headers NewHTTPClient's client reads; net/http's default is 10 MiB.
const maxHeaderBytes = 64 << 10

// GetJSON GETs base+path with the API key header (if key != "") and decodes at most 4 MiB of JSON into v.
// It requires a JSON body but not a JSON Content-Type, since some apps (and test fixture servers)
// do not label plain JSON files correctly.
func GetJSON(ctx context.Context, t Target, path string, keyHeader string, v any) error {
	return StreamJSON(ctx, t, path, keyHeader, func(d *json.Decoder) error {
		if err := d.Decode(v); err != nil {
			return err
		}
		if _, err := d.Token(); err != io.EOF {
			return errors.New("data after the JSON value")
		}
		return nil
	})
}

// StreamJSON GETs base+path like GetJSON and hands the body, of at most 4 MiB, to read as a token stream, so read
// can stop early (see DecodeArray) instead of holding a whole oversized answer in memory. An error from read that
// did not come from reading the body is reported as ErrNotJSON.
func StreamJSON(ctx context.Context, t Target, path string, keyHeader string, read func(*json.Decoder) error) error {
	return StreamJSONLimit(ctx, t, path, keyHeader, maxBodyBytes, read)
}

// MaxStreamBytes bounds the limit StreamJSONLimit accepts.
const MaxStreamBytes = 64 << 20 // 64 MiB

// StreamJSONLimit is StreamJSON with a body limit of limit bytes (at most MaxStreamBytes) instead of 4 MiB, for an
// answer that can legitimately be larger (e.g. a long list an adapter only counts). read must keep a bounded amount
// of what it decodes: the limit bounds what is read, not what read holds on to. A body over the limit is
// ErrTooLarge, which read has seen as a failed read by then; whatever it gathered before that is still its own.
func StreamJSONLimit(ctx context.Context, t Target, path string, keyHeader string, limit int64,
	read func(*json.Decoder) error) error {
	limit = min(max(limit, 1), MaxStreamBytes)
	return doJSON(ctx, t, http.MethodGet, path, nil, limit, func(req *http.Request) {
		if t.APIKey != "" && keyHeader != "" {
			req.Header.Set(keyHeader, t.APIKey)
		}
	}, read)
}

// maxPostBytes bounds the JSON document PostJSON sends.
const maxPostBytes = 64 << 10

// PostJSON POSTs body, a JSON document of at most 64 KiB, to base+path and hands the answer to read like
// StreamJSON. It is for apps whose read-only calls are POSTs (a remote-control API that takes its parameters as a
// JSON body); an adapter must only use it for calls that change nothing. authorize, if not nil, adds the
// credentials to the request; they never appear in an error. A redirect is not followed but answered as a
// StatusError: following it would send the body and credentials to a path the adapter did not choose.
func PostJSON(ctx context.Context, t Target, path string, body []byte, authorize func(*http.Request),
	read func(*json.Decoder) error) error {
	if len(body) > maxPostBytes {
		return errors.New("adapter: request body too large")
	}
	noRedirect := *t.HTTP
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	t.HTTP = &noRedirect
	return doJSON(ctx, t, http.MethodPost, path, body, maxBodyBytes, authorize, read)
}

// doJSON sends one request to base+path, with body (if not nil) as JSON, and hands the answer, of at most limit
// bytes, to read. setHeaders, if not nil, adds the credentials.
func doJSON(ctx context.Context, t Target, method, path string, body []byte, limit int64,
	setHeaders func(*http.Request), read func(*json.Decoder) error) error {
	reqURL, err := joinPath(t.BaseURL, path)
	if err != nil {
		return err
	}

	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL.String(), reqBody)
	if err != nil {
		return fmt.Errorf("adapter: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if setHeaders != nil {
		setHeaders(req)
	}

	resp, err := t.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("adapter: request to %s failed: %w", reqURL.Path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		drain(resp.Body)
		return ErrUnauthorized
	}
	if resp.StatusCode == http.StatusNotFound {
		drain(resp.Body)
		return fmt.Errorf("adapter: %s: %w", reqURL.Path, ErrNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		drain(resp.Body)
		return fmt.Errorf("adapter: %s: %w", reqURL.Path, StatusError(resp.StatusCode))
	}

	answer := &bodyReader{r: http.MaxBytesReader(nil, resp.Body, limit)}
	if err := read(json.NewDecoder(answer)); err != nil {
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(answer.err, &tooLarge):
			return fmt.Errorf("adapter: %s: %w", reqURL.Path, ErrTooLarge)
		case answer.err != nil:
			return fmt.Errorf("adapter: read response from %s: %w", reqURL.Path, answer.err)
		}
		// Deliberately drop the decoder's own message and the body: both can echo back
		// fragments of the response, which must never reach logs or the UI.
		return fmt.Errorf("adapter: %s: %w", reqURL.Path, ErrNotJSON)
	}
	return nil
}

// bodyReader keeps the first error other than io.EOF that reading the body returned, so StreamJSON can tell a failed
// read from malformed JSON.
type bodyReader struct {
	r   io.Reader
	err error
}

func (b *bodyReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err != nil && err != io.EOF && b.err == nil {
		b.err = err
	}
	return n, err
}

// DecodeArray reads a JSON array (or null) from d and hands each element, decoded as T, to each, at most max of
// them. When more follow, it stops there without reading them, reports capped, and d must not be read any further.
func DecodeArray[T any](d *json.Decoder, max int, each func(T)) (capped bool, err error) {
	tok, err := d.Token()
	if err != nil || tok == nil {
		return false, err
	}
	if tok != json.Delim('[') {
		return false, errors.New("not a JSON array")
	}
	for n := 0; d.More(); n++ {
		if n == max {
			return true, nil
		}
		var v T
		if err := d.Decode(&v); err != nil {
			return false, err
		}
		each(v)
	}
	_, err = d.Token() // the closing bracket
	return false, err
}

// drain discards up to maxBodyBytes of body so the connection can be reused, without reading an
// unbounded amount from a hostile server.
func drain(body io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxBodyBytes))
}

// joinPath appends path (which may carry its own query string and escaped characters, e.g. a
// literal "/" encoded as %2F) to base's existing path, so a configured reverse-proxy prefix (e.g.
// "/myapp") is preserved and any escaping in either half survives on the wire.
func joinPath(base *url.URL, path string) (*url.URL, error) {
	rel, err := url.Parse(path)
	if err != nil {
		return nil, fmt.Errorf("adapter: parse request path %q: %w", path, err)
	}

	relPath := withLeadingSlash(rel.Path)
	relEscaped := withLeadingSlash(rel.EscapedPath())
	basePath := strings.TrimSuffix(base.Path, "/")
	baseEscaped := strings.TrimSuffix(base.EscapedPath(), "/")

	u := *base
	u.Path = basePath + relPath
	u.RawPath = baseEscaped + relEscaped
	u.RawQuery = rel.RawQuery
	return &u, nil
}

// withLeadingSlash ensures p starts with "/".
func withLeadingSlash(p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	return "/" + p
}

// maxRedirects bounds the redirect chain NewHTTPClient's client will follow, matching the
// net/http default that is lost once CheckRedirect is set explicitly.
const maxRedirects = 10

// NewHTTPClient returns the HTTP client every adapter must share: a bounded timeout, a small
// per-host pool of idle connections, and a redirect policy that never lets an API key follow a
// redirect to another host. Connections per host are not capped: several services can sit behind
// one reverse proxy, and one that hangs must not hold up the others.
func NewHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		// No Proxy is set on purpose: adapters only ever talk to one configured address, so an
		// environment proxy has no business seeing the request. The whole round trip is bounded
		// by Client.Timeout together with the caller's context, not by these per-phase transport
		// timeouts alone; they just keep a stalled phase (TLS handshake, response headers) from
		// running the clock out silently.
		Transport: &http.Transport{
			DialContext:            (&net.Dialer{Control: refuseLinkLocal}).DialContext,
			MaxIdleConnsPerHost:    4,
			IdleConnTimeout:        90 * time.Second,
			TLSHandshakeTimeout:    10 * time.Second,
			ResponseHeaderTimeout:  10 * time.Second,
			MaxResponseHeaderBytes: maxHeaderBytes,
		},
		CheckRedirect: checkRedirect,
	}
}

var errRefusedAddress = errors.New("adapter: link-local and cloud metadata addresses are not allowed")

// metadataAddrs are cloud metadata services outside the link-local ranges: AWS's IPv6 endpoint and Alibaba
// Cloud's.
var metadataAddrs = []netip.Addr{netip.MustParseAddr("fd00:ec2::254"), netip.MustParseAddr("100.100.100.200")}

// refuseLinkLocal is the dialer's Control function. It refuses link-local destinations (169.254.0.0/16,
// fe80::/10) and metadataAddrs: they are where cloud metadata services live (169.254.169.254 on most clouds),
// which hand out instance credentials to whoever asks from the machine, and no media app lives there. Checking
// the resolved address at dial time also covers host names and redirects that lead there. Private LAN ranges stay
// allowed: that is where the apps run.
func refuseLinkLocal(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("adapter: unexpected dial address %q: %w", address, err)
	}
	if a := ap.Addr().Unmap().WithZone(""); a.IsLinkLocalUnicast() || slices.Contains(metadataAddrs, a) {
		return errRefusedAddress
	}
	return nil
}

// ErrCrossHostRedirect is what NewHTTPClient's client returns (wrapped in a *url.Error) when a
// response redirects to another scheme, host or port. Callers match it with errors.Is: it is the
// client's own policy that stopped the request, not a network failure.
var ErrCrossHostRedirect = errors.New("adapter: refusing a redirect to a different host")

// checkRedirect refuses a redirect whose scheme, host or port differs from the original request
// (via[0]), so a key sent as a header is never sent to another host.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("adapter: stopped after %d redirects", maxRedirects)
	}
	orig := via[0].URL
	if req.URL.Scheme != orig.Scheme || req.URL.Host != orig.Host {
		return ErrCrossHostRedirect
	}
	return nil
}
