package mailer

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeServer speaks just enough SMTP (no TLS, no login) to receive one letter.
func fakeServer(t *testing.T) (port int, received <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	out := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		say := func(s string) { conn.Write([]byte(s + "\r\n")) }
		say("220 fake ESMTP")
		var data strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if inData {
				if line == "." {
					inData = false
					out <- data.String()
					say("250 queued")
					continue
				}
				data.WriteString(line + "\n")
				continue
			}
			switch cmd := strings.ToUpper(line); {
			case strings.HasPrefix(cmd, "EHLO"):
				say("250 fake")
			case strings.HasPrefix(cmd, "MAIL FROM"), strings.HasPrefix(cmd, "RCPT TO"):
				say("250 ok")
			case cmd == "DATA":
				inData = true
				say("354 go ahead")
			case cmd == "QUIT":
				say("221 bye")
				return
			default:
				say("250 ok")
			}
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, out
}

func TestSMTPSendsTheLetter(t *testing.T) {
	port, received := fakeServer(t)
	s := &SMTP{Host: "127.0.0.1", Port: port, From: "food@example.com", Timeout: 5 * time.Second}

	err := s.Send(context.Background(), Message{To: "ana@example.com", Subject: "Cod de confirmare", Body: "Codul tău: K7P2\nValabil 30 de minute."})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		for _, want := range []string{"To: ana@example.com", "From: food@example.com", "K7P2", "Content-Transfer-Encoding: quoted-printable"} {
			if !strings.Contains(got, want) {
				t.Errorf("letter does not contain %q:\n%s", want, got)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the server received nothing")
	}
}

func TestSMTPUnreachableServerFails(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // nothing listens there any more
	s := &SMTP{Host: "127.0.0.1", Port: port, From: "a@b.c", Timeout: time.Second}
	if err := s.Send(context.Background(), Message{To: "x@y.z"}); err == nil {
		t.Fatal("expected an error, port " + strconv.Itoa(port))
	}
}
