/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package channels

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a minimal SMTP server: enough of RFC 5321 for net/smtp to
// deliver one message. It records the DATA it received.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	data []string
}

func startFakeSMTP(t *testing.T, tlsConfig *tls.Config) *fakeSMTP {
	t.Helper()
	var (
		ln  net.Listener
		err error
	)
	if tlsConfig != nil {
		ln, err = tls.Listen("tcp", "127.0.0.1:0", tlsConfig)
	} else {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	say := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			say("250-fake")
			say("250 8BITMIME")
		case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
			say("250 ok")
		case cmd == "DATA":
			say("354 go ahead")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.data = append(f.data, b.String())
			f.mu.Unlock()
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func (f *fakeSMTP) port() string {
	_, port, _ := net.SplitHostPort(f.ln.Addr().String())
	return port
}

func (f *fakeSMTP) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.data...)
}

// selfSignedTLS builds a throwaway certificate for 127.0.0.1, generated at
// test time so no key material lives in the repository.
func selfSignedTLS(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "fake-smtp.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
}

func emailMessage() *NotificationMessage {
	return &NotificationMessage{Title: "db down", Body: "db pods not ready", Severity: "critical", IssueName: "db-outage", Namespace: "prod", State: "Escalated", Timestamp: time.Now()}
}

func TestEmail_PlainSMTPDelivers(t *testing.T) {
	srv := startFakeSMTP(t, nil)
	sender := &EmailSender{config: map[string]string{"smtp_host": "127.0.0.1", "smtp_port": srv.port(), "from": "aiops@example.test", "to": "sre@example.test"}}
	if err := sender.Send(context.Background(), emailMessage()); err != nil {
		t.Fatalf("send: %v", err)
	}
	if msgs := srv.messages(); len(msgs) != 1 || !strings.Contains(msgs[0], "db down") {
		t.Fatalf("server received %q", msgs)
	}
}

// smtp_tls=implicit speaks TLS from the first byte (SMTPS), which a
// STARTTLS-only client cannot do.
func TestEmail_ImplicitTLSDelivers(t *testing.T) {
	srv := startFakeSMTP(t, selfSignedTLS(t))
	sender := &EmailSender{config: map[string]string{
		"smtp_host": "127.0.0.1", "smtp_port": srv.port(), "from": "aiops@example.test", "to": "sre@example.test",
		"smtp_tls": "implicit", "tls_skip_verify": "true",
	}}
	if err := sender.Send(context.Background(), emailMessage()); err != nil {
		t.Fatalf("send over implicit TLS: %v", err)
	}
	if len(srv.messages()) != 1 {
		t.Fatalf("server received %d messages, want 1", len(srv.messages()))
	}
}

func TestEmail_ImplicitTLSSelection(t *testing.T) {
	cases := []struct {
		cfg  map[string]string
		want bool
	}{
		{map[string]string{"smtp_port": "465"}, true},
		{map[string]string{"smtp_port": "587"}, false},
		{map[string]string{"smtp_port": "465", "smtp_tls": "starttls"}, false},
		{map[string]string{"smtp_port": "2525", "smtp_tls": "Implicit"}, true},
	}
	for _, tc := range cases {
		if got := (&EmailSender{config: tc.cfg}).implicitTLS(); got != tc.want {
			t.Errorf("implicitTLS(%v) = %v, want %v", tc.cfg, got, tc.want)
		}
	}
}

// A server that accepts the connection and never greets must not hang the
// sender: smtp_timeout bounds the conversation.
func TestEmail_SilentServerTimesOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		time.Sleep(3 * time.Second)
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	sender := &EmailSender{config: map[string]string{"smtp_host": "127.0.0.1", "smtp_port": port, "from": "a@example.test", "to": "b@example.test", "smtp_timeout": "300ms"}}
	start := time.Now()
	if err := sender.Send(context.Background(), emailMessage()); err == nil {
		t.Fatal("a silent server must fail the send")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("send took %v; the timeout did not apply", elapsed)
	}
	if got := (&EmailSender{config: map[string]string{"smtp_timeout": "nonsense"}}).smtpTimeout(); got != defaultSMTPTimeout {
		t.Fatalf("invalid smtp_timeout = %v, want the default", got)
	}
}
