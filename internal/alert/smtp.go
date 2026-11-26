package alert

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"net/textproto"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// errNoStartTLS means the mail server does not offer STARTTLS on a channel that requires it.
var errNoStartTLS = errors.New("the mail server does not offer STARTTLS; mail is never sent unencrypted")

type email struct {
	s        Settings
	password string
	d        Deps
}

// Send delivers m over SMTP, always encrypted: with "tls" the connection is TLS from the start, with "starttls"
// the server must offer STARTTLS before anything else is said. The password is only ever sent over TLS.
func (e email) Send(ctx context.Context, m Message) error {
	host := strings.Trim(e.s.Host, "[]")
	conn, err := e.d.Dial(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(e.s.Port)))
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(sendTimeout)); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	cfg := &tls.Config{}
	if e.d.TLS != nil {
		cfg = e.d.TLS.Clone()
	}
	cfg.ServerName, cfg.MinVersion = host, tls.VersionTLS12
	if e.s.Security == "tls" {
		tc := tls.Client(conn, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return err
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Hello(heloName(e.s)); err != nil {
		return err
	}
	if e.s.Security == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errNoStartTLS
		}
		if err := c.StartTLS(cfg); err != nil {
			return err
		}
	}
	if _, encrypted := c.TLSConnectionState(); !encrypted {
		return errNoStartTLS // cannot happen: both modes are TLS by now; checked so a change can never send in clear
	}
	if e.s.Username != "" {
		if ok, mechs := c.Extension("AUTH"); !ok || !slices.ContainsFunc(strings.Fields(mechs), func(m string) bool { return strings.EqualFold(m, "PLAIN") }) {
			return errors.New("the mail server does not offer PLAIN sign-in")
		}
		if err := c.Auth(smtp.PlainAuth("", e.s.Username, e.password, host)); err != nil {
			return err
		}
	}
	from, to := addrOf(e.s.From), strings.Split(e.s.To, ", ")
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	body, err := composeMail(e.s.From, e.s.To, m)
	if err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// heloName is the name Observarr greets the mail server with: the one set, else the domain of the From address,
// else this host's name. Some servers refuse the "localhost" Go would say otherwise.
func heloName(s Settings) string {
	if s.Helo != "" {
		return s.Helo
	}
	if _, domain, ok := strings.Cut(addrOf(s.From), "@"); ok && hostName.MatchString(domain) && !strings.HasPrefix(domain, "[") {
		return domain
	}
	if h, err := os.Hostname(); err == nil && hostName.MatchString(h) {
		return h
	}
	return "localhost"
}

// addrOf is the bare address of a checked "Name <address>".
func addrOf(s string) string {
	if i := strings.LastIndexByte(s, '<'); i >= 0 {
		return strings.TrimSuffix(s[i+1:], ">")
	}
	return s
}

// fold writes a header field as name (with its colon) and value, folded before a space so that no line passes 78
// characters where that is possible: an encoded subject is a run of encoded words of at most 75 characters each,
// separated by spaces.
func fold(name, value string) string {
	var b strings.Builder
	b.WriteString(name)
	n := len(name)
	for _, word := range strings.Split(value, " ") {
		if n+1+len(word) > 78 && n > 1 {
			b.WriteString("\r\n ")
			n = 1
		} else {
			b.WriteByte(' ')
			n++
		}
		b.WriteString(word)
		n += len(word)
	}
	return b.String()
}

// composeMail is m as a mail in plain text and simple HTML. Every header value is either checked (the addresses)
// or encoded (the subject), so nothing an app reports can add a header.
func composeMail(from, to string, m Message) ([]byte, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	id := make([]byte, 12)
	rand.Read(id)
	subject := fold("Subject:", mime.QEncoding.Encode("utf-8", "[Observarr] "+strings.Join(strings.Fields(m.heading()), " ")))
	fmt.Fprintf(&buf, "From: %s\r\nTo: %s\r\n%s\r\nDate: %s\r\nMessage-ID: <%s@observarr>\r\n"+
		"MIME-Version: 1.0\r\nAuto-Submitted: auto-generated\r\nContent-Type: multipart/alternative;\r\n boundary=%s\r\n\r\n",
		from, to, subject, m.Time.Format(time.RFC1123Z), hex.EncodeToString(id), mw.Boundary())

	text := m.Text + "\r\n"
	page := "<p>" + html.EscapeString(m.Text) + "</p>"
	if m.URL != "" {
		text += "\r\n" + m.URL + "\r\n"
		page += `<p><a href="` + html.EscapeString(m.URL) + `">Open Observarr</a></p>`
	}
	page = `<!doctype html><html><body style="font-family:sans-serif"><h2 style="font-size:18px">` +
		html.EscapeString(m.heading()) + "</h2>" + page + `<p style="color:#667085;font-size:12px">` +
		html.EscapeString(m.Time.Format("2 Jan 2006 15:04 MST")) + "</p></body></html>"
	for _, part := range []struct{ typ, body string }{{"text/plain", text}, {"text/html", page}} {
		pw, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {part.typ + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"}})
		if err != nil {
			return nil, err
		}
		qp := quotedprintable.NewWriter(pw)
		if _, err := qp.Write([]byte(part.body)); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
