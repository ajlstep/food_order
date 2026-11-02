package mailer

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// SMTP sends through an SMTP server. It uses STARTTLS when the server offers
// it and logs in when a user is set.
type SMTP struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string

	// Timeout limits the whole conversation with the server. net/smtp has no
	// timeouts of its own, and a stuck server must not stuck the service.
	Timeout time.Duration

	// TLSConfig is for tests; nil means the defaults (check the certificate).
	TLSConfig *tls.Config
}

// Send implements Mailer.
func (s *SMTP) Send(ctx context.Context, m Message) error {
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))

	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp: connect to %s: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))

	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp: greeting from %s: %w", addr, err)
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); ok {
		cfg := s.TLSConfig
		if cfg == nil {
			cfg = &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
		}
		if err := c.StartTLS(cfg); err != nil {
			return fmt.Errorf("smtp: starttls: %w", err)
		}
	}
	if s.User != "" {
		// PlainAuth refuses to send the password without TLS (except to localhost).
		if err := c.Auth(smtp.PlainAuth("", s.User, s.Password, s.Host)); err != nil {
			return fmt.Errorf("smtp: login: %w", err)
		}
	}
	if err := c.Mail(s.From); err != nil {
		return fmt.Errorf("smtp: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return fmt.Errorf("smtp: RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp: DATA: %w", err)
	}
	if _, err := w.Write(s.render(m)); err != nil {
		return fmt.Errorf("smtp: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp: end of message: %w", err)
	}
	return c.Quit()
}

// render builds the text of the letter: headers, then the body in
// quoted-printable so that Romanian letters survive any server.
func (s *SMTP) render(m Message) []byte {
	var b bytes.Buffer
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", s.From)
	h("To", m.To)
	h("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	h("Date", time.Now().UTC().Format(time.RFC1123Z))
	h("MIME-Version", "1.0")
	h("Content-Type", `text/plain; charset="utf-8"`)
	h("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	_, _ = qp.Write([]byte(strings.ReplaceAll(m.Body, "\r\n", "\n")))
	_ = qp.Close()
	return b.Bytes()
}
