package netmon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"

	"github.com/henrygd/beszel/internal/entities/monitor"
)

// maxBannerRead bounds the bytes read while looking for a banner.
const maxBannerRead = 1 << 10

// Fixed failure messages of tcp and ssh checks.
var (
	errBannerNotFound = errors.New("banner not found")
	errNotSSH         = errors.New("not an ssh server")
	errSSHVersion     = errors.New("unsupported ssh protocol version")
)

// probeTCP connects to the target, optionally with implicit TLS, and checks
// for an expected banner in the first bytes the server sends.
func probeTCP(ctx context.Context, config monitor.Config) Outcome {
	check := config.Check
	if check == nil {
		check = &monitor.CheckOptions{}
	}
	return probeConn(ctx, config, check.TLS, func(s *connSession) error {
		if check.Banner == "" {
			return nil
		}
		return readBanner(s, check.Banner)
	})
}

// readBanner reads up to maxBannerRead bytes until they contain banner. A
// server that closes the connection or stops sending before the banner
// appears fails with errBannerNotFound once it sent anything.
func readBanner(s *connSession, banner string) error {
	want := []byte(banner)
	received := make([]byte, 0, maxBannerRead)
	chunk := make([]byte, maxBannerRead)
	for len(received) < maxBannerRead {
		n, err := s.reader.Read(chunk[:maxBannerRead-len(received)])
		received = append(received, chunk[:n]...)
		if bytes.Contains(received, want) {
			return nil
		}
		if err != nil {
			if len(received) == 0 && !errors.Is(err, io.EOF) {
				return err
			}
			return errBannerNotFound
		}
	}
	return errBannerNotFound
}

// probeSSH reads the server's identification line. It succeeds for SSH 2
// servers or, with a configured banner, when the line contains it. Servers
// may send other lines before the identification (RFC 4253 section 4.2).
func probeSSH(ctx context.Context, config monitor.Config) Outcome {
	var banner string
	if config.Check != nil {
		banner = config.Check.Banner
	}
	return probeConn(ctx, config, false, func(s *connSession) error {
		read := 0
		for read < maxBannerRead {
			line, err := readLine(s, maxBannerRead-read)
			read += len(line) + 2
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, errLineTooLong) {
					return errNotSSH
				}
				return err
			}
			if !strings.HasPrefix(line, "SSH-") {
				continue
			}
			switch {
			case banner != "":
				if !strings.Contains(line, banner) {
					return errBannerNotFound
				}
			case !strings.HasPrefix(line, "SSH-2.0-") && !strings.HasPrefix(line, "SSH-1.99-"):
				return errSSHVersion
			}
			_ = s.write([]byte("SSH-2.0-InfraScope\r\n"))
			return nil
		}
		return errNotSSH
	})
}

var errLineTooLong = errors.New("line too long")

// readLine reads a line of at most limit bytes without its CRLF or LF ending.
func readLine(s *connSession, limit int) (string, error) {
	var line []byte
	for {
		fragment, isPrefix, err := s.reader.ReadLine()
		line = append(line, fragment...)
		if len(line) > limit {
			return "", errLineTooLong
		}
		if err != nil {
			return "", err
		}
		if !isPrefix {
			return string(line), nil
		}
	}
}
