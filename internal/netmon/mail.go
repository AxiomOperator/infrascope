package netmon

import (
	"context"
	"errors"
	"strings"

	"github.com/henrygd/beszel/internal/entities/monitor"
)

// maxMailLineLen bounds a single SMTP or IMAP response line.
const maxMailLineLen = 4 << 10

// maxMailLines bounds the lines of a multi-line or untagged response.
const maxMailLines = 100

// Fixed failure messages of smtp and imap checks.
var (
	errMailUnexpectedGreeting = errors.New("unexpected greeting")
	errMailRejected           = errors.New("server rejected connection")
	errMailUnexpectedReply    = errors.New("unexpected reply")
	errStartTLSUnsupported    = errors.New("starttls not supported")
	errStartTLSFailed         = errors.New("starttls failed")
	errMailLineTooLong        = errors.New("response line too long")
)

// readMailLine reads one CRLF or LF terminated line without its terminator.
func readMailLine(session *connSession) (string, error) {
	var line []byte
	for {
		chunk, isPrefix, err := session.reader.ReadLine()
		if err != nil {
			return "", err
		}
		line = append(line, chunk...)
		if len(line) > maxMailLineLen {
			return "", errMailLineTooLong
		}
		if !isPrefix {
			return string(line), nil
		}
	}
}

// readSMTPReply reads a possibly multi-line SMTP reply and returns its code
// and the text of each line.
func readSMTPReply(session *connSession) (string, []string, error) {
	var lines []string
	for range maxMailLines {
		line, err := readMailLine(session)
		if err != nil {
			return "", nil, err
		}
		if len(line) < 3 || (len(line) > 3 && line[3] != '-' && line[3] != ' ') {
			return "", nil, errMailUnexpectedReply
		}
		code := line[:3]
		if len(lines) > 0 && code != lines[0][:3] {
			return "", nil, errMailUnexpectedReply
		}
		lines = append(lines, line)
		if len(line) == 3 || line[3] == ' ' {
			texts := make([]string, len(lines))
			for i, l := range lines {
				if len(l) > 4 {
					texts[i] = l[4:]
				}
			}
			return code, texts, nil
		}
	}
	return "", nil, errMailUnexpectedReply
}

// smtpEHLO sends EHLO and returns the extension lines of a 250 reply.
func smtpEHLO(session *connSession) ([]string, error) {
	if err := session.write([]byte("EHLO infrascope\r\n")); err != nil {
		return nil, err
	}
	code, lines, err := readSMTPReply(session)
	if err != nil {
		return nil, err
	}
	if code != "250" {
		return nil, errMailUnexpectedReply
	}
	return lines, nil
}

// probeSMTP reads the greeting, sends EHLO and, when configured, upgrades the
// connection with STARTTLS. Port 465 and the TLS option use implicit TLS.
func probeSMTP(ctx context.Context, config monitor.Config) Outcome {
	check := config.Check
	if check == nil {
		check = &monitor.CheckOptions{}
	}
	implicitTLS := check.TLS || config.Port == 465
	return probeConn(ctx, config, implicitTLS, func(session *connSession) error {
		code, _, err := readSMTPReply(session)
		if err != nil {
			return err
		}
		if code != "220" {
			if code == "554" || code == "421" {
				return errMailRejected
			}
			return errMailUnexpectedGreeting
		}
		extensions, err := smtpEHLO(session)
		if err != nil {
			return err
		}
		if check.StartTLS && !implicitTLS {
			supported := false
			for _, ext := range extensions {
				if strings.EqualFold(strings.TrimSpace(ext), "STARTTLS") {
					supported = true
					break
				}
			}
			if !supported {
				return errStartTLSUnsupported
			}
			if err := session.write([]byte("STARTTLS\r\n")); err != nil {
				return err
			}
			code, _, err := readSMTPReply(session)
			if err != nil {
				return err
			}
			if code != "220" {
				return errStartTLSFailed
			}
			if err := session.upgradeTLS(); err != nil {
				return err
			}
			if _, err := smtpEHLO(session); err != nil {
				return err
			}
		}
		// QUIT is best effort; the check already succeeded.
		if session.write([]byte("QUIT\r\n")) == nil {
			_, _, _ = readSMTPReply(session)
		}
		return nil
	})
}

// probeIMAP reads the greeting and, when configured, upgrades the connection
// with STARTTLS. Port 993 and the TLS option use implicit TLS.
func probeIMAP(ctx context.Context, config monitor.Config) Outcome {
	check := config.Check
	if check == nil {
		check = &monitor.CheckOptions{}
	}
	implicitTLS := check.TLS || config.Port == 993
	return probeConn(ctx, config, implicitTLS, func(session *connSession) error {
		greeting, err := readMailLine(session)
		if err != nil {
			return err
		}
		upper := strings.ToUpper(greeting)
		switch {
		case strings.HasPrefix(upper, "* OK"), strings.HasPrefix(upper, "* PREAUTH"):
		case strings.HasPrefix(upper, "* BYE"):
			return errMailRejected
		default:
			return errMailUnexpectedGreeting
		}
		if check.StartTLS && !implicitTLS {
			if err := session.write([]byte("a1 STARTTLS\r\n")); err != nil {
				return err
			}
			reply, err := readIMAPTagged(session, "a1")
			if err != nil {
				return err
			}
			if !strings.HasPrefix(strings.ToUpper(reply), "A1 OK") {
				return errStartTLSFailed
			}
			if err := session.upgradeTLS(); err != nil {
				return err
			}
		}
		// LOGOUT is best effort; the check already succeeded.
		if session.write([]byte("a2 LOGOUT\r\n")) == nil {
			_, _ = readIMAPTagged(session, "a2")
		}
		return nil
	})
}

// readIMAPTagged reads lines until the tagged response of tag and returns it.
func readIMAPTagged(session *connSession, tag string) (string, error) {
	prefix := strings.ToUpper(tag) + " "
	for range maxMailLines {
		line, err := readMailLine(session)
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(strings.ToUpper(line), prefix) {
			return line, nil
		}
	}
	return "", errMailUnexpectedReply
}
