package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Mailer는 SMTP로 평문(text/plain, UTF-8) 메일을 보낸다. 설정은 KEEL_SMTP_* 환경 변수에서 온다.
type Mailer struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	TLS      string // starttls | tls | none
	Timeout  time.Duration
	// TLSConfig가 있으면 이것을 쓴다(테스트용). 없으면 ServerName=Host, TLS 1.2 이상.
	TLSConfig *tls.Config
}

// MailerFromEnv는 KEEL_SMTP_HOST가 없으면 nil이다.
func MailerFromEnv(getenv func(string) string) (*Mailer, error) {
	host := strings.TrimSpace(getenv("KEEL_SMTP_HOST"))
	if host == "" {
		return nil, nil
	}
	m := &Mailer{Host: host, Username: getenv("KEEL_SMTP_USERNAME"), Password: getenv("KEEL_SMTP_PASSWORD"),
		From: strings.TrimSpace(getenv("KEEL_SMTP_FROM")), TLS: strings.ToLower(strings.TrimSpace(getenv("KEEL_SMTP_TLS")))}
	if m.TLS == "" {
		m.TLS = "starttls"
	}
	switch m.TLS {
	case "starttls":
		m.Port = 587
	case "tls":
		m.Port = 465
	case "none":
		m.Port = 25
	default:
		return nil, errors.New("KEEL_SMTP_TLS must be starttls, tls or none")
	}
	if p := strings.TrimSpace(getenv("KEEL_SMTP_PORT")); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return nil, errors.New("KEEL_SMTP_PORT must be a port number")
		}
		m.Port = n
	}
	if m.From == "" {
		return nil, errors.New("KEEL_SMTP_FROM is required with KEEL_SMTP_HOST")
	}
	if _, err := mail.ParseAddress(m.From); err != nil {
		return nil, fmt.Errorf("KEEL_SMTP_FROM: %w", err)
	}
	return m, nil
}

// ValidAddress는 수신자 주소로 쓸 수 있는지 본다(이름 없는 addr-spec만, 줄바꿈 금지).
func ValidAddress(a string) bool {
	if strings.ContainsAny(a, "\r\n<>,") {
		return false
	}
	p, err := mail.ParseAddress(a)
	return err == nil && p.Address == a
}

// BuildMessage는 RFC 5322 메시지를 만든다. 제목은 RFC 2047로, 본문은 base64로 인코딩한다(한글).
func BuildMessage(from string, to []string, subject, body string, now time.Time) ([]byte, error) {
	fa, err := mail.ParseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	if len(to) == 0 {
		return nil, errors.New("no recipients")
	}
	for _, t := range to {
		if !ValidAddress(t) {
			return nil, fmt.Errorf("invalid recipient %q", t)
		}
	}
	if strings.ContainsAny(subject, "\r\n") {
		return nil, errors.New("subject must be a single line")
	}
	id := make([]byte, 12)
	rand.Read(id)
	domain := "aven"
	if _, d, ok := strings.Cut(fa.Address, "@"); ok {
		domain = d
	}
	var b bytes.Buffer
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", fa.String())
	h("To", strings.Join(to, ", "))
	h("Subject", mime.BEncoding.Encode("utf-8", subject))
	h("Date", now.Format(time.RFC1123Z))
	h("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	h("MIME-Version", "1.0")
	h("Content-Type", "text/plain; charset=utf-8")
	h("Content-Transfer-Encoding", "base64")
	h("Auto-Submitted", "auto-generated")
	b.WriteString("\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(body, "\n", "\r\n")))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return b.Bytes(), nil
}

// Send는 메일 한 통을 보낸다.
func (m *Mailer) Send(ctx context.Context, to []string, subject, body string) error {
	msg, err := BuildMessage(m.From, to, subject, body, time.Now())
	if err != nil {
		return err
	}
	fa, _ := mail.ParseAddress(m.From)
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	addr := net.JoinHostPort(m.Host, strconv.Itoa(m.Port))
	tcfg := m.TLSConfig
	if tcfg == nil {
		tcfg = &tls.Config{ServerName: m.Host, MinVersion: tls.VersionTLS12}
	}
	d := &net.Dialer{Timeout: timeout}
	var conn net.Conn
	if m.TLS == "tls" {
		conn, err = (&tls.Dialer{NetDialer: d, Config: tcfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	deadline := time.Now().Add(2 * timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if err := c.Hello("aven"); err != nil {
		return err
	}
	if m.TLS == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("SMTP 서버가 STARTTLS를 지원하지 않습니다 (KEEL_SMTP_TLS=none으로 끌 수 있습니다)")
		}
		if err := c.StartTLS(tcfg); err != nil {
			return err
		}
	}
	if m.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("SMTP 서버가 AUTH를 지원하지 않습니다")
		}
		if err := c.Auth(smtp.PlainAuth("", m.Username, m.Password, m.Host)); err != nil {
			return err
		}
	}
	if err := c.Mail(fa.Address); err != nil {
		return err
	}
	for _, t := range to {
		if err := c.Rcpt(t); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
