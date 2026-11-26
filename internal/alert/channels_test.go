package alert

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// receiver records the requests a fake destination gets and answers each with its status.
type receiver struct {
	*httptest.Server
	mu     sync.Mutex
	reqs   []*http.Request
	bodies [][]byte
	status atomic.Int32
}

func newReceiver(t *testing.T) *receiver {
	r := &receiver{}
	r.status.Store(http.StatusOK)
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.reqs, r.bodies = append(r.reqs, req), append(r.bodies, body)
		r.mu.Unlock()
		w.WriteHeader(int(r.status.Load()))
		w.Write([]byte(`{"error":"echo ` + req.URL.Path + `"}`))
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *receiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}

func (r *receiver) last(t *testing.T) (*http.Request, map[string]any) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reqs) == 0 {
		t.Fatal("nothing was received")
	}
	var body map[string]any
	if err := json.Unmarshal(r.bodies[len(r.bodies)-1], &body); err != nil {
		t.Fatalf("body is not JSON: %s", r.bodies[len(r.bodies)-1])
	}
	return r.reqs[len(r.reqs)-1], body
}

var sample = Message{Event: EventFiring, Rule: RuleDown, Severity: Critical, Name: "Sintel", Title: "Sintel is down",
	Text: "connection refused @everyone", Time: t0, URL: "http://observarr.lan/#/apps"}

func sender(t *testing.T, typ string, s Settings, sec Secrets) Sender {
	t.Helper()
	s, err := CheckChannel(typ, s, sec)
	if err != nil {
		t.Fatal(err)
	}
	return NewSender(typ, s, sec, Deps{HTTP: NewHTTPClient(), TelegramAPI: "http://unused.invalid"})
}

func TestNtfyPayload(t *testing.T) {
	r := newReceiver(t)
	s := sender(t, "ntfy", Settings{Server: r.URL + "/", Topic: "observarr-alerts"}, Secrets{"token": "tk_abc123"})
	if err := s.Send(context.Background(), sample); err != nil {
		t.Fatal(err)
	}
	req, body := r.last(t)
	if req.URL.Path != "/" || req.Header.Get("Authorization") != "Bearer tk_abc123" {
		t.Fatalf("request = %s %v", req.URL.Path, req.Header)
	}
	if body["topic"] != "observarr-alerts" || body["title"] != "Sintel is down" || body["priority"] != 5.0 ||
		body["tags"].([]any)[0] != "rotating_light" || body["click"] != sample.URL {
		t.Fatalf("body = %v", body)
	}
	resolved := sample
	resolved.Event = EventResolved
	s.Send(context.Background(), resolved)
	if _, body := r.last(t); body["title"] != "Resolved: Sintel is down" || body["tags"].([]any)[0] != "white_check_mark" {
		t.Fatalf("resolved body = %v", body)
	}
}

func TestDiscordPayload(t *testing.T) {
	r := newReceiver(t)
	tls := httptest.NewTLSServer(r.Config.Handler)
	t.Cleanup(tls.Close)
	sec := Secrets{"url": tls.URL + "/api/webhooks/1/secret-token"}
	s, err := CheckChannel("discord", Settings{}, sec)
	if err != nil {
		t.Fatal(err)
	}
	snd := NewSender("discord", s, sec, Deps{HTTP: tls.Client()})
	if err := snd.Send(context.Background(), sample); err != nil {
		t.Fatal(err)
	}
	_, body := r.last(t)
	embed := body["embeds"].([]any)[0].(map[string]any)
	if embed["color"] != float64(colourCritical) || embed["title"] != "Sintel is down" ||
		embed["timestamp"] != "2026-11-26T"+t0.UTC().Format("15:04:05")+"Z" {
		t.Fatalf("embed = %v", embed)
	}
	if parse := body["allowed_mentions"].(map[string]any)["parse"].([]any); len(parse) != 0 {
		t.Fatal("mentions are allowed")
	}
	if _, err := CheckChannel("discord", Settings{}, Secrets{"url": "http://discord.example/api/webhooks/1/x"}); err == nil {
		t.Fatal("a plain http webhook was accepted")
	}
}

func TestWebhookPayloadAndSignature(t *testing.T) {
	r := newReceiver(t)
	s := sender(t, "webhook", Settings{}, Secrets{"url": r.URL + "/hook?token=abc", "secret": "hmac-secret"})
	if err := s.Send(context.Background(), sample); err != nil {
		t.Fatal(err)
	}
	req, body := r.last(t)
	r.mu.Lock()
	raw := r.bodies[0]
	r.mu.Unlock()
	mac := hmac.New(sha256.New, []byte("hmac-secret"))
	mac.Write(raw)
	if got := req.Header.Get(SignatureHeader); got != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("signature = %q", got)
	}
	if req.URL.RawQuery != "token=abc" {
		t.Fatalf("query = %q", req.URL.RawQuery)
	}
	want := map[string]any{"event": "firing", "severity": "critical", "service": "Sintel", "title": "Sintel is down",
		"message": "connection refused @everyone", "time": t0.UTC().Format("2006-01-02T15:04:05Z"), "url": sample.URL}
	if len(body) != len(want) {
		t.Fatalf("body = %v", body)
	}
	for k, v := range want {
		if body[k] != v {
			t.Fatalf("%s = %v, want %v", k, body[k], v)
		}
	}
	unsigned := sender(t, "webhook", Settings{}, Secrets{"url": r.URL})
	unsigned.Send(context.Background(), sample)
	if req, _ := r.last(t); req.Header.Get(SignatureHeader) != "" {
		t.Fatal("signed without a secret")
	}
}

func TestGotifyAndTelegram(t *testing.T) {
	r := newReceiver(t)
	g := sender(t, "gotify", Settings{Server: r.URL}, Secrets{"token": "AbCdEf123"})
	if err := g.Send(context.Background(), sample); err != nil {
		t.Fatal(err)
	}
	if req, body := r.last(t); req.URL.Path != "/message" || req.Header.Get("X-Gotify-Key") != "AbCdEf123" ||
		body["priority"] != 8.0 {
		t.Fatalf("gotify: %s %v %v", req.URL.Path, req.Header, body)
	}

	token := "123456:AAbbCCddEEffGGhhIIjjKKllMMnn"
	sec := Secrets{"token": token}
	s, err := CheckChannel("telegram", Settings{ChatID: "-1001234"}, sec)
	if err != nil {
		t.Fatal(err)
	}
	tg := NewSender("telegram", s, sec, Deps{HTTP: NewHTTPClient(), TelegramAPI: r.URL})
	if err := tg.Send(context.Background(), sample); err != nil {
		t.Fatal(err)
	}
	if req, body := r.last(t); req.URL.Path != "/bot"+token+"/sendMessage" || body["chat_id"] != "-1001234" ||
		!strings.HasPrefix(body["text"].(string), "Sintel is down\n") || body["parse_mode"] != nil {
		t.Fatalf("telegram: %s %v", req.URL.Path, body)
	}
	// The answer echoes the path, token and all: the status line must not.
	r.status.Store(http.StatusUnauthorized)
	err = tg.Send(context.Background(), sample)
	if note := describe(err, []string{token}); strings.Contains(note, token) || strings.Contains(note, "AAbbCC") ||
		!strings.HasPrefix(note, "HTTP 401 Unauthorized") {
		t.Fatalf("note = %q", note)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	elsewhere := newReceiver(t)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/stolen", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	s := sender(t, "webhook", Settings{}, Secrets{"url": redirect.URL, "secret": "s3cret"})
	err := s.Send(context.Background(), sample)
	if err == nil || !permanent(err) || elsewhere.count() != 0 {
		t.Fatalf("Send = %v, followed: %v", err, elsewhere.count() != 0)
	}
	if note := describe(err, nil); !strings.Contains(note, "redirects are not followed") {
		t.Fatalf("note = %q", note)
	}
}

func TestMetadataAddressesAreRefused(t *testing.T) {
	for _, u := range []string{"http://169.254.169.254/latest/meta-data/", "http://[fd00:ec2::254]/", "http://100.100.100.200/"} {
		s := sender(t, "webhook", Settings{}, Secrets{"url": u})
		err := s.Send(context.Background(), sample)
		if note := describe(err, nil); !permanent(err) || !strings.HasPrefix(note, "refused") {
			t.Errorf("%s: %v (%q)", u, err, note)
		}
	}
}

func TestLongAnswerMakesAShortNote(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("\x1b[31m" + strings.Repeat("noise ", 200_000)))
	}))
	t.Cleanup(srv.Close)
	err := sender(t, "webhook", Settings{}, Secrets{"url": srv.URL}).Send(context.Background(), sample)
	note := describe(err, nil)
	if permanent(err) || len([]rune(note)) > maxNote || strings.ContainsRune(note, 0x1b) ||
		!strings.HasPrefix(note, "HTTP 500 Internal Server Error: [31mnoise noise") {
		t.Fatalf("note = %q (permanent %v)", note, permanent(err))
	}
}

func TestCheckChannel(t *testing.T) {
	good := []struct {
		typ string
		s   Settings
		sec Secrets
	}{
		{"ntfy", Settings{Topic: "alerts"}, nil},
		{"gotify", Settings{Server: "http://192.168.1.5:8080"}, Secrets{"token": "abc"}},
		{"email", Settings{Host: "mail.example.com", Security: "starttls", From: "Observarr <o@example.com>",
			To: "alex@example.com, sam@example.com"}, nil},
	}
	for _, c := range good {
		if _, err := CheckChannel(c.typ, c.s, c.sec); err != nil {
			t.Errorf("%s: %v", c.typ, err)
		}
	}
	if s, _ := CheckChannel("ntfy", Settings{Topic: "a"}, nil); s.Server != "https://ntfy.sh" {
		t.Errorf("ntfy default server = %q", s.Server)
	}
	if s, _ := CheckChannel("email", good[2].s, nil); s.Port != 587 || s.To != "alex@example.com, sam@example.com" {
		t.Errorf("email = %+v", s)
	}
	bad := map[string]struct {
		typ string
		s   Settings
		sec Secrets
	}{
		"unknown type":      {"pager", Settings{}, nil},
		"setting of other":  {"ntfy", Settings{Topic: "a", Host: "x"}, nil},
		"unknown secret":    {"ntfy", Settings{Topic: "a"}, Secrets{"password": "x"}},
		"topic":             {"ntfy", Settings{Topic: "a b"}, nil},
		"server user":       {"ntfy", Settings{Server: "https://u:p@ntfy.example", Topic: "a"}, nil},
		"server scheme":     {"gotify", Settings{Server: "ftp://g"}, Secrets{"token": "a"}},
		"missing secret":    {"gotify", Settings{Server: "http://g"}, nil},
		"webhook userinfo":  {"webhook", Settings{}, Secrets{"url": "https://u:p@hook.example/x"}},
		"webhook fragment":  {"webhook", Settings{}, Secrets{"url": "https://hook.example/x#y"}},
		"telegram token":    {"telegram", Settings{ChatID: "1"}, Secrets{"token": "nope"}},
		"plain email":       {"email", Settings{Host: "m", Security: "none", From: "a@b", To: "c@d"}, nil},
		"user without pass": {"email", Settings{Host: "m", Security: "tls", Username: "u", From: "a@b", To: "c@d"}, nil},
		"header injection":  {"email", Settings{Host: "m", Security: "tls", From: "a@b\r\nBcc: x@y", To: "c@d"}, nil},
		"control in secret": {"ntfy", Settings{Topic: "a"}, Secrets{"token": "a\nb"}},
	}
	for name, c := range bad {
		if _, err := CheckChannel(c.typ, c.s, c.sec); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
