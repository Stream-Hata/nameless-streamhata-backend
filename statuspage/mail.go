package statuspage

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

type Mailer struct {
	host     string
	port     int
	user     string
	password string
	from     string
	to       []string
	mode     string
}

func NewMailer(password string) *Mailer {
	port, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("SMTP_PORT")))
	if port == 0 {
		port = 587
	}
	to := make([]string, 0)
	for _, value := range strings.Split(os.Getenv("INCIDENT_EMAIL_TO"), ",") {
		if value = strings.TrimSpace(value); value != "" {
			to = append(to, value)
		}
	}

	return &Mailer{
		host:     strings.TrimSpace(os.Getenv("SMTP_HOST")),
		port:     port,
		user:     strings.TrimSpace(os.Getenv("SMTP_USER")),
		password: password,
		from:     strings.TrimSpace(os.Getenv("SMTP_FROM")),
		to:       to,
		mode:     strings.ToLower(strings.TrimSpace(os.Getenv("SMTP_TLS_MODE"))),
	}
}

func (m *Mailer) Enabled() bool {
	return m != nil && m.host != "" && m.from != "" && len(m.to) > 0
}

func (m *Mailer) Send(subject, body string) error {
	if !m.Enabled() {
		return nil
	}
	address := net.JoinHostPort(m.host, strconv.Itoa(m.port))
	client, err := m.connect(address)
	if err != nil {
		return err
	}
	defer client.Close()

	if m.user != "" {
		auth := smtp.PlainAuth("", m.user, m.password, m.host)
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(m.from); err != nil {
		return err
	}
	for _, recipient := range m.to {
		if err := client.Rcpt(recipient); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	message := "From: " + m.from + "\r\n" +
		"To: " + strings.Join(m.to, ", ") + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"Date: " + time.Now().Format(time.RFC1123Z) + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n\r\n" + body
	if _, err := writer.Write([]byte(message)); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	return client.Quit()
}

func (m *Mailer) connect(address string) (*smtp.Client, error) {
	if m.mode == "ssl" || m.port == 465 {
		connection, err := tls.Dial("tcp", address, &tls.Config{
			ServerName: m.host,
			MinVersion: tls.VersionTLS12,
		})
		if err != nil {
			return nil, err
		}

		return smtp.NewClient(connection, m.host)
	}
	client, err := smtp.Dial(address)
	if err != nil {
		return nil, err
	}
	if m.mode != "none" {
		if err := client.StartTLS(&tls.Config{ServerName: m.host, MinVersion: tls.VersionTLS12}); err != nil {
			_ = client.Close()

			return nil, fmt.Errorf("smtp starttls: %w", err)
		}
	}

	return client, nil
}
