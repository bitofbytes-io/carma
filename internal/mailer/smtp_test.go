package mailer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func TestWriteMessageUsesSafeHeadersAndHidesEnvelopeRecipients(t *testing.T) {
	var output bytes.Buffer
	message := Message{Recipients: []string{"private1@example.com", "private2@example.com"}, Subject: "Carma maintenance reminder", Body: "Line one\nLine two"}
	err := writeMessage(&output, time.Date(2026, time.April, 1, 12, 0, 0, 0, time.UTC), "Carma", "carma@bitofbytes.io", "<id@bitofbytes.io>", message)
	if err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, expected := range []string{"From: \"Carma\" <carma@bitofbytes.io>\r\n", "To: undisclosed-recipients:;\r\n", "Message-ID: <id@bitofbytes.io>\r\n", "Line one\r\nLine two"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("message missing %q:\n%s", expected, got)
		}
	}
	if strings.Contains(got, "private1@example.com") || strings.Contains(got, "private2@example.com") {
		t.Fatal("envelope recipients leaked into message headers")
	}
}

func TestWriteMessageRejectsHeaderInjection(t *testing.T) {
	if err := writeMessage(&bytes.Buffer{}, time.Now(), "Carma", "carma@bitofbytes.io", "<id@example.com>", Message{Subject: "safe\r\nBcc: victim@example.com"}); err == nil {
		t.Fatal("header injection accepted")
	}
}

type smtpCapture struct {
	recipients []string
	data       string
}

func TestSMTPImplicitTLSAuthenticatedDelivery(t *testing.T) {
	serverTLS, roots := testTLSCertificate(t)
	address, capture, done := startSMTPServer(t, serverTLS, "carma", "password", false)
	sender, err := NewSMTP(address, "carma", "password", "carma@bitofbytes.io", "Carma")
	if err != nil {
		t.Fatal(err)
	}
	sender.tlsConfig.RootCAs = roots
	sender.newID = func() string { return "generated-id" }
	messageID, err := sender.Send(context.Background(), Message{Recipients: []string{"one@example.com", "two@example.com"}, Subject: "Carma maintenance reminder", Body: "delivery body"})
	if err != nil {
		t.Fatal(err)
	}
	if messageID != "<generated-id@bitofbytes.io>" {
		t.Fatalf("message id = %q", messageID)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := <-capture
	if strings.Join(got.recipients, ",") != "one@example.com,two@example.com" || !strings.Contains(got.data, "delivery body") {
		t.Fatalf("capture = %+v", got)
	}
}

func TestSMTPRejectsUntrustedCertificateAndWrongHostname(t *testing.T) {
	serverTLS, roots := testTLSCertificate(t)
	t.Run("untrusted certificate", func(t *testing.T) {
		address, _, done := startSMTPServer(t, serverTLS, "carma", "password", false)
		sender, _ := NewSMTP(address, "carma", "password", "carma@bitofbytes.io", "Carma")
		if _, err := sender.Send(context.Background(), Message{Recipients: []string{"one@example.com"}, Subject: "subject"}); err == nil || !strings.Contains(err.Error(), "TLS handshake") {
			t.Fatalf("error = %v", err)
		}
		<-done // the peer observes the rejected handshake
	})
	t.Run("wrong hostname", func(t *testing.T) {
		address, _, done := startSMTPServer(t, serverTLS, "carma", "password", false)
		sender, _ := NewSMTP(address, "carma", "password", "carma@bitofbytes.io", "Carma")
		sender.tlsConfig.RootCAs = roots
		sender.tlsConfig.ServerName = "wrong.example"
		if _, err := sender.Send(context.Background(), Message{Recipients: []string{"one@example.com"}, Subject: "subject"}); err == nil || !strings.Contains(err.Error(), "TLS handshake") {
			t.Fatalf("error = %v", err)
		}
		<-done
	})
}

func TestSMTPAuthenticationFailure(t *testing.T) {
	serverTLS, roots := testTLSCertificate(t)
	address, _, done := startSMTPServer(t, serverTLS, "carma", "correct", false)
	sender, _ := NewSMTP(address, "carma", "wrong", "carma@bitofbytes.io", "Carma")
	sender.tlsConfig.RootCAs = roots
	if _, err := sender.Send(context.Background(), Message{Recipients: []string{"one@example.com"}, Subject: "subject"}); err == nil || !strings.Contains(err.Error(), "authentication") {
		t.Fatalf("error = %v", err)
	}
	if err := <-done; err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}

func TestSMTPAcceptedMessageSurvivesQUITFailure(t *testing.T) {
	serverTLS, roots := testTLSCertificate(t)
	address, capture, done := startSMTPServer(t, serverTLS, "carma", "password", true)
	sender, _ := NewSMTP(address, "carma", "password", "carma@bitofbytes.io", "Carma")
	sender.tlsConfig.RootCAs = roots
	messageID, err := sender.Send(context.Background(), Message{Recipients: []string{"one@example.com"}, Subject: "subject", Body: "accepted"})
	if err != nil || messageID == "" {
		t.Fatalf("messageID=%q error=%v", messageID, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := <-capture; !strings.Contains(got.data, "accepted") {
		t.Fatalf("DATA was not captured: %+v", got)
	}
}

func TestSMTPContextBoundsUnresponsiveTLSHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- connection
		}
	}()
	sender, _ := NewSMTP(listener.Addr().String(), "carma", "password", "carma@bitofbytes.io", "Carma")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err = sender.Send(ctx, Message{Recipients: []string{"one@example.com"}, Subject: "subject"}); err == nil {
		t.Fatal("unresponsive TLS peer did not fail")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
	select {
	case connection := <-accepted:
		_ = connection.Close()
	case <-time.After(time.Second):
		t.Fatal("test server did not accept connection")
	}
}

func testTLSCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	now := time.Now()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Carma test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	parsedCA, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	serverDER, err := x509.CreateCertificate(rand.Reader, server, ca, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsedCA)
	return certificate, roots
}

func startSMTPServer(t *testing.T, certificate tls.Certificate, username, password string, failQuit bool) (string, <-chan smtpCapture, <-chan error) {
	t.Helper()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	capture := make(chan smtpCapture, 1)
	done := make(chan error, 1)
	go func() {
		defer listener.Close()
		connection, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer connection.Close()
		reader, writer := bufio.NewReader(connection), bufio.NewWriter(connection)
		writeReply := func(reply string) error {
			if _, err := writer.WriteString(reply); err != nil {
				return err
			}
			return writer.Flush()
		}
		if err = writeReply("220 localhost ESMTP ready\r\n"); err != nil {
			done <- err
			return
		}
		var got smtpCapture
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				done <- readErr
				return
			}
			command := strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(command, "EHLO "):
				err = writeReply("250-localhost\r\n250-AUTH PLAIN\r\n250 OK\r\n")
			case strings.HasPrefix(command, "AUTH PLAIN "):
				decoded, decodeErr := base64.StdEncoding.DecodeString(strings.TrimPrefix(command, "AUTH PLAIN "))
				if decodeErr == nil && string(decoded) == "\x00"+username+"\x00"+password {
					err = writeReply("235 authenticated\r\n")
				} else {
					err = writeReply("535 authentication failed\r\n")
				}
			case strings.HasPrefix(command, "MAIL FROM:"):
				err = writeReply("250 sender ok\r\n")
			case strings.HasPrefix(command, "RCPT TO:"):
				got.recipients = append(got.recipients, strings.Trim(strings.TrimPrefix(command, "RCPT TO:"), "<>"))
				err = writeReply("250 recipient ok\r\n")
			case command == "DATA":
				if err = writeReply("354 send data\r\n"); err == nil {
					var data strings.Builder
					for {
						dataLine, dataErr := reader.ReadString('\n')
						if dataErr != nil {
							err = dataErr
							break
						}
						if dataLine == ".\r\n" {
							break
						}
						data.WriteString(dataLine)
					}
					got.data = data.String()
					capture <- got
					if err == nil {
						err = writeReply("250 accepted\r\n")
					}
				}
			case command == "QUIT":
				if failQuit {
					err = writeReply("421 quit failed\r\n")
				} else {
					err = writeReply("221 goodbye\r\n")
				}
				done <- err
				return
			default:
				err = writeReply("500 unsupported\r\n")
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()
	return listener.Addr().String(), capture, done
}
