// Package mailer sends email. The rest of the service only knows the Mailer
// interface, so the way letters leave (a console in development, an SMTP
// server in production, a fake in tests) can change without touching it.
package mailer

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// Message is one plain-text letter.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Mailer sends one message. It returns when the message is handed over.
type Mailer interface {
	Send(ctx context.Context, m Message) error
}

// Console prints the letter instead of sending it. It is for development,
// where there is no mail server; it is the one place where a confirmation
// code is meant to be visible.
type Console struct {
	mu  sync.Mutex
	Out io.Writer
}

// Send implements Mailer.
func (c *Console) Send(_ context.Context, m Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := fmt.Fprintf(c.Out, "---- email (console mailer, nothing was sent) ----\nTo: %s\nSubject: %s\n\n%s\n----\n", m.To, m.Subject, m.Body)
	return err
}

// Fake keeps the letters in memory, for tests.
type Fake struct {
	mu   sync.Mutex
	sent []Message
	Err  error // if set, Send fails with it
}

// Send implements Mailer.
func (f *Fake) Send(_ context.Context, m Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return f.Err
	}
	f.sent = append(f.sent, m)
	return nil
}

// Sent returns the letters sent so far.
func (f *Fake) Sent() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Message(nil), f.sent...)
}
