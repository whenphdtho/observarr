package alert

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/mail"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"observarr/internal/adapter"
	"observarr/internal/secret"
)

// Message is one alert as a channel delivers it.
type Message struct {
	Event    string    `json:"event"`
	Rule     Rule      `json:"rule"`
	Severity Severity  `json:"severity"`
	Name     string    `json:"name"` // what it is about, e.g. a service's name; "" for none
	Title    string    `json:"title"`
	Text     string    `json:"text"`
	Time     time.Time `json:"time"`
	URL      string    `json:"url"` // where Observarr shows the details; "" without Rules.Link
}

// heading is the title a person reads: what happened to the alert, then the alert.
func (m Message) heading() string {
	switch m.Event {
	case EventResolved:
		return "Resolved: " + m.Title
	case EventRepeat:
		return "Still: " + m.Title
	}
	return m.Title
}

// Sender delivers messages to one channel.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// Settings are a channel's settings that are not secret. Each type uses some of them.
type Settings struct {
	Server   string `json:"server,omitempty"`   // ntfy, gotify
	Topic    string `json:"topic,omitempty"`    // ntfy
	ChatID   string `json:"chatId,omitempty"`   // telegram
	Host     string `json:"host,omitempty"`     // email
	Port     int    `json:"port,omitempty"`     // email
	Security string `json:"security,omitempty"` // email: "starttls" or "tls"
	Username string `json:"username,omitempty"` // email
	From     string `json:"from,omitempty"`     // email
	To       string `json:"to,omitempty"`       // email: addresses separated by commas
}

// Secrets are a channel's secret fields by name: tokens, addresses holding a token, passwords.
type Secrets map[string]string

// Deps is what senders need from outside.
type Deps struct {
	HTTP *http.Client
	// Dial opens mail connections; it must refuse what NewHTTPClient's dialer refuses.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// TLS is the base TLS configuration of mail connections (tests trust their own certificate through it).
	TLS *tls.Config
	// TelegramAPI is the bot API's address; tests point it elsewhere.
	TelegramAPI string
}

// DefaultDeps are the senders' dependencies in production.
func DefaultDeps() Deps {
	return Deps{HTTP: NewHTTPClient(), Dial: adapter.Dialer(10 * time.Second).DialContext,
		TelegramAPI: "https://api.telegram.org"}
}

// kind is one channel type.
type kind struct {
	settings []string // the Settings fields it uses, by JSON name
	secrets  []string // its secret fields
	required []string // of them, the ones it cannot do without
	// check validates and normalises the settings; checkSecrets validates the secrets. Either may be nil.
	check        func(s *Settings) error
	checkSecrets func(s Settings, sec Secrets) error
	// target sums up where messages go, with nothing secret in it.
	target func(s Settings, sec Secrets) string
	sender func(s Settings, sec Secrets, d Deps) Sender
}

// Types lists the channel types.
var Types = []string{"ntfy", "discord", "webhook", "email", "gotify", "telegram"}

var kinds = map[string]kind{
	"ntfy": {settings: []string{"server", "topic"}, secrets: []string{"token"},
		check: checkNtfy, checkSecrets: tokenWithoutSpaces, target: func(s Settings, _ Secrets) string { return hostOf(s.Server) + "/" + s.Topic },
		sender: func(s Settings, sec Secrets, d Deps) Sender { return ntfy{s, sec["token"], d.HTTP} }},
	"discord": {secrets: []string{"url"}, required: []string{"url"},
		checkSecrets: checkDiscord, target: func(_ Settings, sec Secrets) string { return "webhook on " + hostOf(sec["url"]) },
		sender: func(_ Settings, sec Secrets, d Deps) Sender { return discord{sec["url"], d.HTTP} }},
	"webhook": {secrets: []string{"url", "secret"}, required: []string{"url"},
		checkSecrets: checkWebhook, target: func(_ Settings, sec Secrets) string { return originOf(sec["url"]) },
		sender: func(_ Settings, sec Secrets, d Deps) Sender { return webhook{sec["url"], sec["secret"], d.HTTP} }},
	"email": {settings: []string{"host", "port", "security", "username", "from", "to"}, secrets: []string{"password"},
		check: checkEmail, checkSecrets: checkEmailPassword, target: func(s Settings, _ Secrets) string { return s.To },
		sender: func(s Settings, sec Secrets, d Deps) Sender { return email{s, sec["password"], d} }},
	"gotify": {settings: []string{"server"}, secrets: []string{"token"}, required: []string{"token"},
		check: checkGotify, checkSecrets: tokenWithoutSpaces, target: func(s Settings, _ Secrets) string { return hostOf(s.Server) },
		sender: func(s Settings, sec Secrets, d Deps) Sender { return gotify{s.Server, sec["token"], d.HTTP} }},
	"telegram": {settings: []string{"chatId"}, secrets: []string{"token"}, required: []string{"token"},
		check: checkTelegram, checkSecrets: checkTelegramToken, target: func(s Settings, _ Secrets) string { return "chat " + s.ChatID },
		sender: func(s Settings, sec Secrets, d Deps) Sender { return telegram{s.ChatID, sec["token"], d} }},
}

// maxSecret bounds a secret field, in bytes.
const maxSecret = 2048

// CheckChannel validates a channel of type typ and returns its settings normalised: every setting it does not use
// is refused, as is any secret it does not know, and every secret it requires must be there.
func CheckChannel(typ string, s Settings, sec Secrets) (Settings, error) {
	s, err := CheckSettings(typ, s)
	if err != nil {
		return Settings{}, err
	}
	k := kinds[typ]
	for name, v := range sec {
		switch {
		case !slices.Contains(k.secrets, name):
			return Settings{}, fmt.Errorf("%s: not a secret of a %s channel", name, typ)
		case len(v) > maxSecret:
			return Settings{}, fmt.Errorf("%s: at most %d bytes", name, maxSecret)
		case strings.ContainsFunc(v, unicode.IsControl) || !utf8.ValidString(v):
			return Settings{}, fmt.Errorf("%s: no control characters", name)
		}
	}
	for _, name := range k.required {
		if sec[name] == "" {
			return Settings{}, fmt.Errorf("%s: required", name)
		}
	}
	if k.checkSecrets != nil {
		if err := k.checkSecrets(s, sec); err != nil {
			return Settings{}, err
		}
	}
	return s, nil
}

// CheckSettings validates and normalises the settings of a channel of type typ alone, for a channel whose secrets
// must be entered again.
func CheckSettings(typ string, s Settings) (Settings, error) {
	k, ok := kinds[typ]
	if !ok {
		return Settings{}, fmt.Errorf("type: one of %s", strings.Join(Types, ", "))
	}
	for name, v := range settingFields(&s) {
		if !slices.Contains(k.settings, name) && !isZero(v) {
			return Settings{}, fmt.Errorf("%s: not a setting of a %s channel", name, typ)
		}
	}
	if k.check != nil {
		if err := k.check(&s); err != nil {
			return Settings{}, err
		}
	}
	return s, nil
}

// SecretNames lists the secret fields of channel type typ.
func SecretNames(typ string) []string { return kinds[typ].secrets }

// Target sums up where a channel's messages go, with nothing secret in it.
func Target(typ string, s Settings, sec Secrets) string {
	if k, ok := kinds[typ]; ok {
		return k.target(s, sec)
	}
	return ""
}

// NewSender returns the sender of a checked channel.
func NewSender(typ string, s Settings, sec Secrets, d Deps) Sender {
	return kinds[typ].sender(s, sec, d)
}

func settingFields(s *Settings) map[string]any {
	return map[string]any{"server": s.Server, "topic": s.Topic, "chatId": s.ChatID, "host": s.Host, "port": s.Port,
		"security": s.Security, "username": s.Username, "from": s.From, "to": s.To}
}

func isZero(v any) bool { return v == "" || v == 0 }

// ParseSettings reads settings JSON strictly.
func ParseSettings(body []byte) (Settings, error) {
	var s Settings
	if len(body) == 0 {
		return s, nil
	}
	err := decodeStrict(body, &s)
	return s, err
}

// SecretsContext binds a channel's sealed secrets to its row, so they cannot be moved to another one.
func SecretsContext(id int64) string { return "alert-channel:" + strconv.FormatInt(id, 10) }

// SealSecrets seals sec for channel id; no secrets are stored as nil.
func SealSecrets(box *secret.Box, id int64, sec Secrets) ([]byte, error) {
	sec = maps.Clone(sec)
	maps.DeleteFunc(sec, func(_, v string) bool { return v == "" })
	if len(sec) == 0 {
		return nil, nil
	}
	plain, err := json.Marshal(sec)
	if err != nil {
		return nil, err
	}
	return box.Seal(plain, SecretsContext(id))
}

// ErrSecretsUnreadable means a channel's secrets cannot be decrypted, e.g. after the key file was replaced.
var ErrSecretsUnreadable = errors.New("the channel's secrets cannot be decrypted; enter them again")

// OpenSecrets opens what SealSecrets sealed for channel id.
func OpenSecrets(box *secret.Box, id int64, sealed []byte) (Secrets, error) {
	sec := Secrets{}
	if sealed == nil {
		return sec, nil
	}
	plain, err := box.Open(sealed, SecretsContext(id))
	if err != nil || json.Unmarshal(plain, &sec) != nil {
		return nil, ErrSecretsUnreadable
	}
	return sec, nil
}

// Validation of each type.

var (
	ntfyTopic     = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)
	telegramToken = regexp.MustCompile(`^[0-9]{3,20}:[A-Za-z0-9_-]{20,100}$`)
	telegramChat  = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z0-9_]{5,32})$`)
	hostName      = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$|^\[[0-9A-Fa-f:.]+\]$`)
)

func checkServer(server *string, def string) error {
	if *server == "" {
		*server = def
	}
	if *server == "" {
		return errors.New("server: required")
	}
	u, err := adapter.ParseBaseURL(*server)
	if err != nil {
		return errors.New("server: an http:// or https:// address, without user name, query or fragment")
	}
	*server = u.String()
	return nil
}

func tokenWithoutSpaces(_ Settings, sec Secrets) error {
	if strings.ContainsFunc(sec["token"], unicode.IsSpace) {
		return errors.New("token: no spaces")
	}
	return nil
}

func checkNtfy(s *Settings) error {
	if err := checkServer(&s.Server, "https://ntfy.sh"); err != nil {
		return err
	}
	if !ntfyTopic.MatchString(s.Topic) {
		return errors.New("topic: 1 to 64 letters, digits, - or _")
	}
	return nil
}

func checkGotify(s *Settings) error { return checkServer(&s.Server, "") }

func checkDiscord(_ Settings, sec Secrets) error {
	u, err := checkURL(sec["url"], true)
	if err != nil || u.Scheme != "https" || !strings.HasPrefix(u.Path, "/api/webhooks/") {
		return errors.New("url: the webhook address, https://…/api/webhooks/…")
	}
	return nil
}

func checkWebhook(_ Settings, sec Secrets) error {
	if _, err := checkURL(sec["url"], true); err != nil {
		return fmt.Errorf("url: %w", err)
	}
	return nil
}

func checkTelegram(s *Settings) error {
	if !telegramChat.MatchString(s.ChatID) {
		return errors.New("chatId: a chat number or @channelname")
	}
	return nil
}

func checkTelegramToken(_ Settings, sec Secrets) error {
	if !telegramToken.MatchString(sec["token"]) {
		return errors.New("token: the bot token, as 123456:ABC…")
	}
	return nil
}

// maxRecipients bounds the addresses an email channel sends to.
const maxRecipients = 10

func checkEmail(s *Settings) error {
	if !hostName.MatchString(s.Host) {
		return errors.New("host: the mail server's host name or address")
	}
	switch s.Security {
	case "starttls":
		s.Port = cmp.Or(s.Port, 587)
	case "tls":
		s.Port = cmp.Or(s.Port, 465)
	default:
		return errors.New(`security: "starttls" or "tls"; mail is never sent unencrypted`)
	}
	if s.Port < 1 || s.Port > 65535 {
		return errors.New("port: 1 to 65535")
	}
	if len(s.Username) > 256 || strings.ContainsFunc(s.Username, unicode.IsControl) {
		return errors.New("username: at most 256 bytes, no control characters")
	}
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return errors.New("from: one address")
	}
	s.From = from.String()
	to, err := mail.ParseAddressList(s.To)
	if err != nil || len(to) == 0 || len(to) > maxRecipients {
		return fmt.Errorf("to: 1 to %d addresses, separated by commas", maxRecipients)
	}
	list := make([]string, len(to))
	for i, a := range to {
		list[i] = a.Address
	}
	s.To = strings.Join(list, ", ")
	return nil
}

func checkEmailPassword(s Settings, sec Secrets) error {
	if (s.Username == "") != (sec["password"] == "") {
		return errors.New("username and password: both or neither")
	}
	return nil
}

// hostOf is the host (and port) of an address, "" when it is none.
func hostOf(raw string) string {
	u, err := checkURL(raw, true)
	if err != nil {
		return ""
	}
	return u.Host
}

// originOf is the scheme and host of an address, "" when it is none.
func originOf(raw string) string {
	u, err := checkURL(raw, true)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// The senders.

type ntfy struct {
	s     Settings
	token string
	http  *http.Client
}

func (n ntfy) Send(ctx context.Context, m Message) error {
	priority, tag := 3, "information_source"
	switch {
	case m.Event == EventResolved:
		tag = "white_check_mark"
	case m.Severity == Critical:
		priority, tag = 5, "rotating_light"
	case m.Severity == Warning:
		priority, tag = 4, "warning"
	}
	body, err := json.Marshal(map[string]any{"topic": n.s.Topic, "title": m.heading(), "message": m.Text,
		"priority": priority, "tags": []string{tag}, "click": m.URL})
	if err != nil {
		return err
	}
	h := http.Header{}
	if n.token != "" {
		h.Set("Authorization", "Bearer "+n.token)
	}
	return post(ctx, n.http, n.s.Server+"/", h, body)
}

type discord struct {
	url  string
	http *http.Client
}

// Embed colours by severity, and for an alert that is over.
const (
	colourCritical = 0xD92D20
	colourWarning  = 0xDC6803
	colourInfo     = 0x2E90FA
	colourResolved = 0x079455
)

func (d discord) Send(ctx context.Context, m Message) error {
	colour := colourInfo
	switch {
	case m.Event == EventResolved:
		colour = colourResolved
	case m.Severity == Critical:
		colour = colourCritical
	case m.Severity == Warning:
		colour = colourWarning
	}
	embed := map[string]any{"title": clip(m.heading(), 256), "description": clip(m.Text, 4000), "color": colour,
		"timestamp": m.Time.UTC().Format(time.RFC3339)}
	if m.URL != "" {
		embed["url"] = m.URL
	}
	// No mentions: text an app reports must never ping anyone.
	body, err := json.Marshal(map[string]any{"username": "Observarr", "embeds": []any{embed},
		"allowed_mentions": map[string]any{"parse": []string{}}})
	if err != nil {
		return err
	}
	return post(ctx, d.http, d.url, http.Header{}, body)
}

type webhook struct {
	url, secret string
	http        *http.Client
}

// WebhookPayload is the JSON a webhook channel posts; docs/configuration.md documents it.
type WebhookPayload struct {
	Event    string   `json:"event"`
	Severity Severity `json:"severity"`
	Service  string   `json:"service"`
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Time     string   `json:"time"`
	URL      string   `json:"url"`
}

// SignatureHeader carries a webhook's HMAC-SHA256 of the body, as "sha256=<hex>", when it has a secret.
const SignatureHeader = "X-Observarr-Signature"

func (w webhook) Send(ctx context.Context, m Message) error {
	body, err := json.Marshal(WebhookPayload{Event: m.Event, Severity: m.Severity, Service: m.Name, Title: m.Title,
		Message: m.Text, Time: m.Time.UTC().Format(time.RFC3339), URL: m.URL})
	if err != nil {
		return err
	}
	h := http.Header{}
	if w.secret != "" {
		mac := hmac.New(sha256.New, []byte(w.secret))
		mac.Write(body)
		h.Set(SignatureHeader, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	return post(ctx, w.http, w.url, h, body)
}

type gotify struct {
	server, token string
	http          *http.Client
}

func (g gotify) Send(ctx context.Context, m Message) error {
	priority := 2
	switch {
	case m.Event == EventResolved:
	case m.Severity == Critical:
		priority = 8
	case m.Severity == Warning:
		priority = 5
	}
	text := m.Text
	if m.URL != "" {
		text += "\n" + m.URL
	}
	body, err := json.Marshal(map[string]any{"title": m.heading(), "message": text, "priority": priority})
	if err != nil {
		return err
	}
	h := http.Header{}
	h.Set("X-Gotify-Key", g.token)
	return post(ctx, g.http, g.server+"/message", h, body)
}

type telegram struct {
	chat, token string
	d           Deps
}

func (t telegram) Send(ctx context.Context, m Message) error {
	text := m.heading() + "\n" + m.Text
	if m.URL != "" {
		text += "\n" + m.URL
	}
	// Plain text, no parse mode: nothing an app reports can format or link anything.
	body, err := json.Marshal(map[string]any{"chat_id": t.chat, "text": clip(text, 4000),
		"disable_web_page_preview": true})
	if err != nil {
		return err
	}
	return post(ctx, t.d.HTTP, t.d.TelegramAPI+"/bot"+t.token+"/sendMessage", http.Header{}, body)
}

// clip cuts s to at most n characters.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
