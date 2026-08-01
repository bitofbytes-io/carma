package mailer

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const defaultTimeout = 15 * time.Second

type Message struct {
	Recipients []string
	Subject    string
	Body       string
}

type Sender interface {
	Send(context.Context, Message) (string, error)
}

type SMTP struct {
	address, host, username, password, fromAddress, fromName string
	timeout                                                  time.Duration
	now                                                      func() time.Time
	newID                                                    func() string
	tlsConfig                                                *tls.Config
}

func NewSMTP(address, username, password, fromAddress, fromName string) (*SMTP, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("SMTP address: %w", err)
	}
	return &SMTP{
		address: address, host: host, username: username, password: password,
		fromAddress: fromAddress, fromName: fromName, timeout: defaultTimeout,
		now: time.Now, newID: func() string { return uuid.NewString() },
		tlsConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}, nil
}

func (s *SMTP) Send(ctx context.Context, message Message) (messageID string, err error) {
	if len(message.Recipients) == 0 {
		return "", errors.New("SMTP message has no recipients")
	}
	dialer := net.Dialer{Timeout: s.timeout}
	raw, err := dialer.DialContext(ctx, "tcp", s.address)
	if err != nil {
		return "", fmt.Errorf("SMTP connect: %w", err)
	}
	defer raw.Close()
	deadline := s.now().Add(s.timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err = raw.SetDeadline(deadline); err != nil {
		return "", fmt.Errorf("SMTP deadline: %w", err)
	}
	stopClose := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stopClose()

	tlsConfiguration := s.tlsConfig.Clone()
	if tlsConfiguration.ServerName == "" {
		tlsConfiguration.ServerName = s.host
	}
	tlsConnection := tls.Client(raw, tlsConfiguration)
	if err = tlsConnection.HandshakeContext(ctx); err != nil {
		return "", fmt.Errorf("SMTP TLS handshake: %w", err)
	}
	client, err := smtp.NewClient(tlsConnection, s.host)
	if err != nil {
		return "", fmt.Errorf("SMTP greeting: %w", err)
	}
	defer client.Close()
	if err = client.Auth(smtp.PlainAuth("", s.username, s.password, s.host)); err != nil {
		return "", fmt.Errorf("SMTP authentication: %w", err)
	}
	if err = client.Mail(s.fromAddress); err != nil {
		return "", fmt.Errorf("SMTP sender: %w", err)
	}
	for _, recipient := range message.Recipients {
		if err = client.Rcpt(recipient); err != nil {
			// Relay responses can echo the envelope address, so do not expose
			// the raw response to application logs.
			return "", errors.New("SMTP recipient rejected")
		}
	}
	messageID = fmt.Sprintf("<%s@%s>", s.newID(), messageIDDomain(s.fromAddress))
	data, err := client.Data()
	if err != nil {
		return "", fmt.Errorf("SMTP DATA: %w", err)
	}
	if err = writeMessage(data, s.now(), s.fromName, s.fromAddress, messageID, message); err != nil {
		_ = data.Close()
		return "", err
	}
	if err = data.Close(); err != nil {
		return "", fmt.Errorf("SMTP message acceptance: %w", err)
	}
	// DATA completion is the SMTP acceptance point. A subsequent QUIT failure
	// must not turn an accepted message into an unaudited retry.
	_ = client.Quit()
	return messageID, nil
}

func writeMessage(writer io.Writer, now time.Time, fromName, fromAddress, messageID string, message Message) error {
	if strings.ContainsAny(message.Subject, "\r\n") {
		return errors.New("SMTP subject contains an invalid newline")
	}
	buffer := bufio.NewWriter(writer)
	from := (&mail.Address{Name: fromName, Address: fromAddress}).String()
	headers := []string{
		"From: " + from,
		"To: undisclosed-recipients:;",
		"Date: " + now.UTC().Format(time.RFC1123Z),
		"Message-ID: " + messageID,
		"Subject: " + message.Subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: 8bit",
	}
	for _, header := range headers {
		if _, err := fmt.Fprintf(buffer, "%s\r\n", header); err != nil {
			return fmt.Errorf("write SMTP headers: %w", err)
		}
	}
	if _, err := fmt.Fprintf(buffer, "\r\n%s", normalizeCRLF(message.Body)); err != nil {
		return fmt.Errorf("write SMTP body: %w", err)
	}
	if err := buffer.Flush(); err != nil {
		return fmt.Errorf("flush SMTP message: %w", err)
	}
	return nil
}

func normalizeCRLF(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.ReplaceAll(value, "\n", "\r\n")
}

func messageIDDomain(address string) string {
	if parsed, err := mail.ParseAddress(address); err == nil {
		if _, domain, found := strings.Cut(parsed.Address, "@"); found && domain != "" {
			return domain
		}
	}
	return "localhost"
}
