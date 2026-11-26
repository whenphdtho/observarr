package alert

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"observarr/internal/adapter"
)

// fakeSMTP is a tiny mail server: it offers STARTTLS or not, or speaks TLS from the start, and records every
// command with whether the connection was encrypted when it came.
type fakeSMTP struct {
	ln       net.Listener
	startTLS bool
	implicit bool
	tls      *tls.Config
	mu       sync.Mutex
	cmds     []string
	data     string
}

// testCert returns a certificate for 127.0.0.1 and a pool that trusts it.
func testCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	return srv.TLS.Certificates[0], srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
}

func newFakeSMTP(t *testing.T, startTLS, implicit bool) (*fakeSMTP, Deps, Settings) {
	cert, pool := testCert(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, startTLS: startTLS, implicit: implicit, tls: &tls.Config{Certificates: []tls.Certificate{cert}}}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	security := "starttls"
	if implicit {
		security = "tls"
	}
	s := Settings{Host: "127.0.0.1", Port: port, Security: security, Username: "alex", From: "Observarr <o@example.com>",
		To: "alex@example.com"}
	return f, Deps{Dial: adapter.Dialer(time.Second).DialContext, TLS: &tls.Config{RootCAs: pool}}, s
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	encrypted := false
	if f.implicit {
		conn, encrypted = tls.Server(conn, f.tls), true
	}
	r := textproto.NewReader(bufio.NewReader(conn))
	say := func(s string) { conn.Write([]byte(s + "\r\n")) }
	say("220 fake ESMTP")
	for {
		line, err := r.ReadLine()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.cmds = append(f.cmds, strconv.FormatBool(encrypted)+" "+line)
		f.mu.Unlock()
		switch verb, _, _ := strings.Cut(strings.ToUpper(line), " "); verb {
		case "EHLO":
			say("250-fake")
			if f.startTLS && !encrypted {
				say("250-STARTTLS")
			}
			say("250 AUTH PLAIN")
		case "STARTTLS":
			say("220 go ahead")
			conn, encrypted = tls.Server(conn, f.tls), true
			r = textproto.NewReader(bufio.NewReader(conn))
		case "AUTH":
			say("235 accepted")
		case "DATA":
			say("354 go ahead")
			lines, _ := r.ReadDotLines()
			f.mu.Lock()
			f.data = strings.Join(lines, "\n")
			f.mu.Unlock()
			say("250 queued")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func (f *fakeSMTP) received() ([]string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...), f.data
}

func TestEmailOverStartTLS(t *testing.T) {
	f, d, s := newFakeSMTP(t, true, false)
	sec := Secrets{"password": "mail-password"}
	if _, err := CheckChannel("email", s, sec); err != nil {
		t.Fatal(err)
	}
	if err := NewSender("email", s, sec, d).Send(context.Background(), sample); err != nil {
		t.Fatal(err)
	}
	cmds, data := f.received()
	for _, c := range cmds {
		if strings.HasPrefix(c, "false ") && !strings.HasPrefix(c, "false EHLO") && c != "false STARTTLS" {
			t.Errorf("sent before encryption: %q", c)
		}
	}
	if !strings.Contains(strings.Join(cmds, "\n"), "true AUTH PLAIN") {
		t.Errorf("no sign-in over TLS: %q", cmds)
	}
	for _, want := range []string{"Subject: [Observarr] Sintel is down", "To: alex@example.com",
		"Content-Type: text/plain; charset=utf-8", "Content-Type: text/html; charset=utf-8",
		"Sintel is down</h2><p>connection refused", "Auto-Submitted: auto-generated"} {
		if !strings.Contains(data, want) {
			t.Errorf("mail lacks %q:\n%s", want, data)
		}
	}
}

func TestEmailNeverSignsInWithoutTLS(t *testing.T) {
	f, d, s := newFakeSMTP(t, false, false) // the server does not offer STARTTLS
	err := NewSender("email", s, Secrets{"password": "mail-password"}, d).Send(context.Background(), sample)
	if err != errNoStartTLS || !permanent(err) {
		t.Fatalf("Send = %v, want errNoStartTLS", err)
	}
	cmds, _ := f.received()
	for _, c := range cmds {
		if !strings.HasPrefix(c, "false EHLO") && c != "false QUIT" {
			t.Errorf("said more than hello to a server without STARTTLS: %q", cmds)
		}
	}
}

func TestEmailOverImplicitTLS(t *testing.T) {
	f, d, s := newFakeSMTP(t, false, true)
	if err := NewSender("email", s, Secrets{"password": "pw"}, d).Send(context.Background(), sample); err != nil {
		t.Fatal(err)
	}
	if cmds, _ := f.received(); !strings.Contains(strings.Join(cmds, "\n"), "true AUTH PLAIN") {
		t.Fatalf("commands = %q", cmds)
	}
}

func TestEmailUntrustedCertificateFails(t *testing.T) {
	_, d, s := newFakeSMTP(t, true, false)
	d.TLS = nil // the system roots do not know the test certificate
	err := NewSender("email", s, Secrets{"password": "pw"}, d).Send(context.Background(), sample)
	if note := describe(err, nil); note != "the TLS certificate is not trusted for this host" {
		t.Fatalf("note = %q (%v)", note, err)
	}
}

func TestEmailDialRefusesMetadataAddresses(t *testing.T) {
	s := Settings{Host: "169.254.169.254", Port: 25, Security: "starttls", From: "o@example.com", To: "a@example.com"}
	d := Deps{Dial: adapter.Dialer(time.Second).DialContext}
	err := NewSender("email", s, nil, d).Send(context.Background(), sample)
	if !adapter.IsRefusedAddress(err) {
		t.Fatalf("Send = %v, want refused", err)
	}
}

// The server is greeted with the name set, else the From address's domain, never "localhost".
func TestEmailGreetsWithAName(t *testing.T) {
	f, d, s := newFakeSMTP(t, true, false)
	sec := Secrets{"password": "mail-password"}
	if err := NewSender("email", s, sec, d).Send(context.Background(), sample); err != nil {
		t.Fatal(err)
	}
	if cmds, _ := f.received(); cmds[0] != "false EHLO example.com" {
		t.Fatalf("greeting = %q", cmds[0])
	}
	s.Helo = "observarr.example.net"
	if err := NewSender("email", s, sec, d).Send(context.Background(), sample); err != nil {
		t.Fatal(err)
	}
	if cmds, _ := f.received(); !strings.Contains(strings.Join(cmds, "\n"), "false EHLO observarr.example.net") {
		t.Fatalf("greeting = %q", cmds)
	}
}

// A title that holds a line break cannot add a header, and a long subject is folded into lines that fit.
func TestMailHeadersHoldOnlyWhatTheyShould(t *testing.T) {
	m := sample
	m.Title = "Sintel\r\nBcc: someone@example.net\r\n\r\nis down " + strings.Repeat("ünïcödé ", 30)
	raw, err := composeMail("Observarr <o@example.com>", "alex@example.com", m)
	if err != nil {
		t.Fatal(err)
	}
	head, _, _ := strings.Cut(string(raw), "\r\n\r\n")
	for line := range strings.SplitSeq(head, "\r\n") {
		if len(line) > 78 {
			t.Errorf("header line of %d characters: %q", len(line), line)
		}
		if strings.HasPrefix(strings.ToLower(line), "bcc:") {
			t.Errorf("a title added a header: %q", line)
		}
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || !strings.HasPrefix(subject, "[Observarr] Sintel Bcc: someone@example.net is down ünïcödé") ||
		len(msg.Header["Bcc"]) != 0 {
		t.Fatalf("subject = %q (%v), header = %v", subject, err, msg.Header)
	}
}
