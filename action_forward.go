package main

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// ForwardAction 把邮件经 SMTP 转发到目标邮箱:正文为原文摘要,
// attachOriginal 时把原始邮件附为 .eml(默认不附)。
type ForwardAction struct {
	smtp           *SMTPConfig
	to             []string
	attachOriginal bool
}

func (a *ForwardAction) Name() string   { return "forward" }
func (a *ForwardAction) Target() string { return strings.Join(a.to, ", ") }

func (a *ForwardAction) Execute(m *Mail) error {
	msg := a.buildMessage(m)
	return sendSMTP(a.smtp, a.to, msg)
}

func (a *ForwardAction) buildMessage(m *Mail) []byte {
	boundary := fmt.Sprintf("mailwatch-%d-%d", m.UID, time.Now().UnixNano())
	subject := mime.BEncoding.Encode("UTF-8", "Fwd: "+m.Subject)

	noBodyHint := "(无文本正文)"
	if a.attachOriginal {
		noBodyHint = "(无文本正文,见附件原始邮件)"
	}
	summary := fmt.Sprintf(
		"---------- 转发的邮件 ----------\n发件人: %s\n日期: %s\n主题: %s\n收件人: %s\n--------------------------------\n\n%s",
		m.From, m.Date, m.Subject, m.To, orDefault(m.Body, noBodyHint))

	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\r\n", args...) }
	w("From: %s", a.smtp.From)
	w("To: %s", strings.Join(a.to, ", "))
	w("Subject: %s", subject)
	w("Date: %s", time.Now().Format(time.RFC1123Z))
	w("MIME-Version: 1.0")
	if !a.attachOriginal {
		w("Content-Type: text/plain; charset=utf-8")
		w("Content-Transfer-Encoding: base64")
		w("")
		w("%s", wrapBase64(summary))
		return []byte(b.String())
	}
	w("Content-Type: multipart/mixed; boundary=%q", boundary)
	w("")
	w("--%s", boundary)
	w("Content-Type: text/plain; charset=utf-8")
	w("Content-Transfer-Encoding: base64")
	w("")
	w("%s", wrapBase64(summary))
	w("--%s", boundary)
	w("Content-Type: message/rfc822")
	w("Content-Disposition: attachment; filename=\"original.eml\"")
	w("")
	b.Write(m.Raw)
	w("")
	w("--%s--", boundary)
	return []byte(b.String())
}

func wrapBase64(s string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc)
	return b.String()
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// connectSMTP 建连 + TLS + 认证,后台"测试发信"也复用。
func connectSMTP(cfg *SMTPConfig) (*smtp.Client, error) {
	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))
	dialer := &net.Dialer{Timeout: 30 * time.Second}

	var c *smtp.Client
	if cfg.SSL {
		conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: cfg.Host})
		if err != nil {
			return nil, fmt.Errorf("SMTPS 连接: %w", err)
		}
		c, err = smtp.NewClient(conn, cfg.Host)
		if err != nil {
			conn.Close()
			return nil, err
		}
	} else {
		conn, err := dialer.Dial("tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("SMTP 连接: %w", err)
		}
		c, err = smtp.NewClient(conn, cfg.Host)
		if err != nil {
			conn.Close()
			return nil, err
		}
		if err := c.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
			c.Close()
			return nil, fmt.Errorf("STARTTLS: %w", err)
		}
	}
	if err := c.Auth(smtp.PlainAuth("", cfg.User, cfg.Password, cfg.Host)); err != nil {
		c.Close()
		return nil, fmt.Errorf("SMTP 认证: %w", err)
	}
	return c, nil
}

func sendSMTP(cfg *SMTPConfig, rcpts []string, msg []byte) error {
	c, err := connectSMTP(cfg)
	if err != nil {
		return err
	}
	defer c.Close()

	if err := c.Mail(cfg.From); err != nil {
		return err
	}
	for _, r := range rcpts {
		if err := c.Rcpt(r); err != nil {
			return fmt.Errorf("RCPT %s: %w", r, err)
		}
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write(msg); err != nil {
		wc.Close()
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return c.Quit()
}
