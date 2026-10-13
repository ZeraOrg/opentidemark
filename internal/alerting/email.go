// SPDX-License-Identifier: Apache-2.0

package alerting

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/ZeraOrg/tidemark/internal/meta"
)

// SMTPConfig is the global SMTP configuration (SPEC §7 TIDEMARK_SMTP_*).
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	StartTLS bool
	// TLS overrides the STARTTLS client configuration (tests); nil = verify against Host.
	TLS *tls.Config
}

// Configured reports whether email can be sent.
func (c SMTPConfig) Configured() bool { return c.Host != "" && c.From != "" }

// ErrStartTLSUnavailable is returned when STARTTLS is required but not offered.
var ErrStartTLSUnavailable = errors.New("SMTP server does not offer STARTTLS")

// Email sends plain-text + HTML mail over SMTP (STARTTLS required unless disabled).
type Email struct {
	SMTP SMTPConfig
	Now  func() time.Time
}

// Kind implements Notifier.
func (Email) Kind() string { return KindEmail }

// Notify implements Notifier.
func (e Email) Notify(ctx context.Context, ch meta.Channel, n Notification) error {
	c, err := decodeConfig(ch)
	if err != nil {
		return err
	}
	if !e.SMTP.Configured() || len(c.To) == 0 {
		return &PermanentError{Err: errors.New("email: SMTP or recipients not configured")}
	}
	msg, err := e.message(c.To, n)
	if err != nil {
		return &PermanentError{Err: err}
	}
	return e.send(ctx, c.To, msg)
}

func (e Email) send(ctx context.Context, to []string, msg []byte) error {
	addr := net.JoinHostPort(e.SMTP.Host, strconv.Itoa(e.SMTP.Port))
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl) // the context deadline bounds the whole SMTP dialogue
	}
	client, err := smtp.NewClient(conn, e.SMTP.Host)
	if err != nil {
		_ = conn.Close() // the handshake error is reported
		return fmt.Errorf("smtp: %w", err)
	}
	defer client.Close()
	if err := e.secure(client); err != nil {
		return err
	}
	if err := client.Mail(e.SMTP.From); err != nil {
		return fmt.Errorf("smtp MAIL: %w", err)
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp RCPT: %w", err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp DATA end: %w", err)
	}
	return client.Quit()
}

// secure upgrades with STARTTLS (required unless disabled) and authenticates.
func (e Email) secure(c *smtp.Client) error {
	if e.SMTP.StartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return &PermanentError{Err: ErrStartTLSUnavailable}
		}
		cfg := e.SMTP.TLS
		if cfg == nil {
			cfg = &tls.Config{ServerName: e.SMTP.Host, MinVersion: tls.VersionTLS12}
		}
		if err := c.StartTLS(cfg); err != nil {
			return fmt.Errorf("smtp STARTTLS: %w", err)
		}
	}
	if e.SMTP.Username == "" {
		return nil
	}
	// PlainAuth refuses to send credentials over an unencrypted connection to a remote host.
	if err := c.Auth(smtp.PlainAuth("", e.SMTP.Username, e.SMTP.Password, e.SMTP.Host)); err != nil {
		return &PermanentError{Err: fmt.Errorf("smtp AUTH: %w", err)}
	}
	return nil
}

var emailHTML = template.Must(template.New("mail").Parse(`<!doctype html><html><body style="font-family:sans-serif">
<h2>{{.Summary}}</h2>
<table cellpadding="4">{{range .Facts}}<tr><th align="left">{{index . 0}}</th><td>{{index . 1}}</td></tr>{{end}}</table>
{{if .Text}}<p style="white-space:pre-wrap">{{.Text}}</p>{{end}}
{{if .URL}}<p><a href="{{.URL}}">Open in Tidemark</a></p>{{end}}
</body></html>`))

// message renders the MIME message (multipart/alternative: text/plain + text/html).
func (e Email) message(to []string, n Notification) ([]byte, error) {
	text := strings.TrimSpace(n.Description + "\n" + n.Message)
	var plain strings.Builder
	plain.WriteString(summary(n) + "\n\n")
	for _, f := range facts(n) {
		plain.WriteString(f[0] + ": " + f[1] + "\n")
	}
	if text != "" {
		plain.WriteString("\n" + text + "\n")
	}
	if n.URL != "" {
		plain.WriteString("\n" + n.URL + "\n")
	}
	var html bytes.Buffer
	if err := emailHTML.Execute(&html, map[string]any{"Summary": summary(n), "Facts": facts(n), "Text": text, "URL": n.URL}); err != nil {
		return nil, err
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, part := range []struct{ ctype, content string }{{"text/plain; charset=utf-8", plain.String()}, {"text/html; charset=utf-8", html.String()}} {
		w, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {part.ctype}, "Content-Transfer-Encoding": {"quoted-printable"}})
		if err != nil {
			return nil, err
		}
		qp := quotedprintable.NewWriter(w)
		if _, err := qp.Write([]byte(part.content)); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	var msg bytes.Buffer
	now := time.Now
	if e.Now != nil {
		now = e.Now
	}
	headers := [][2]string{
		{"From", e.SMTP.From},
		{"To", strings.Join(to, ", ")},
		{"Subject", mime.QEncoding.Encode("utf-8", headerSafe(summary(n)))},
		{"Date", now().UTC().Format(time.RFC1123Z)},
		{"Message-ID", messageID(e.SMTP.Host)},
		{"MIME-Version", "1.0"},
		{"Content-Type", "multipart/alternative; boundary=" + mw.Boundary()},
	}
	for _, h := range headers {
		msg.WriteString(h[0] + ": " + h[1] + "\r\n")
	}
	msg.WriteString("\r\n")
	msg.Write(body.Bytes())
	return msg.Bytes(), nil
}

// headerSafe removes characters that could inject header lines.
func headerSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return ' '
		}
		return r
	}, s)
}

func messageID(host string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand never fails on supported platforms
	return "<" + hex.EncodeToString(b) + "@" + headerSafe(host) + ">"
}
