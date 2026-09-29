package netmon

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/henrygd/beszel/internal/entities/monitor"
)

const (
	// maxMinecraftStatus bounds the status JSON, which may embed a favicon.
	maxMinecraftStatus = 512 << 10
	// a2sMaxChallenges bounds challenge rounds of an A2S_INFO query.
	a2sMaxChallenges = 2
	maxA2SPacket     = 1400 * 4
)

// Fixed failure messages of game server checks.
var (
	errMinecraftInvalid = errors.New("invalid status response")
	errA2SInvalid       = errors.New("invalid response")
	errA2SSplit         = errors.New("unsupported split response")
)

// appendVarInt appends a Minecraft protocol VarInt.
func appendVarInt(buf []byte, value int32) []byte {
	v := uint32(value)
	for {
		if v&^0x7f == 0 {
			return append(buf, byte(v))
		}
		buf = append(buf, byte(v&0x7f|0x80))
		v >>= 7
	}
}

// readVarInt reads a Minecraft protocol VarInt of at most 5 bytes.
func readVarInt(r io.ByteReader) (int32, error) {
	var value uint32
	for i := range 5 {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		value |= uint32(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			return int32(value), nil
		}
	}
	return 0, errMinecraftInvalid
}

// minecraftPacket prefixes a packet body with its VarInt length.
func minecraftPacket(body []byte) []byte {
	return append(appendVarInt(nil, int32(len(body))), body...)
}

// probeMinecraft performs a Java Edition Server List Ping and requires a
// status JSON object with version information.
func probeMinecraft(ctx context.Context, config monitor.Config) Outcome {
	return probeConn(ctx, config, false, func(session *connSession) error {
		handshake := appendVarInt(nil, 0x00)
		handshake = appendVarInt(handshake, -1)
		handshake = appendVarInt(handshake, int32(len(config.Target)))
		handshake = append(handshake, config.Target...)
		handshake = binary.BigEndian.AppendUint16(handshake, config.Port)
		handshake = appendVarInt(handshake, 1)
		request := append(minecraftPacket(handshake), minecraftPacket([]byte{0x00})...)
		if err := session.write(request); err != nil {
			return err
		}
		length, err := readVarInt(session.reader)
		if err != nil {
			return minecraftReadErr(err)
		}
		if length <= 0 || length > maxMinecraftStatus+10 {
			return errMinecraftInvalid
		}
		packet := make([]byte, length)
		if _, err := io.ReadFull(session.reader, packet); err != nil {
			return minecraftReadErr(err)
		}
		reader := bytes.NewReader(packet)
		if id, err := readVarInt(reader); err != nil || id != 0 {
			return errMinecraftInvalid
		}
		size, err := readVarInt(reader)
		if err != nil || size <= 0 || int(size) != reader.Len() {
			return errMinecraftInvalid
		}
		var status struct {
			Version *struct {
				Name     *string `json:"name"`
				Protocol *int    `json:"protocol"`
			} `json:"version"`
		}
		if err := json.Unmarshal(packet[len(packet)-int(size):], &status); err != nil {
			return errMinecraftInvalid
		}
		if status.Version == nil || (status.Version.Name == nil && status.Version.Protocol == nil) {
			return errMinecraftInvalid
		}
		return nil
	})
}

// minecraftReadErr keeps timeouts and reports other read failures as invalid.
func minecraftReadErr(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return errMinecraftInvalid
	}
	return err
}

var a2sInfoQuery = append([]byte{0xff, 0xff, 0xff, 0xff, 'T'}, "Source Engine Query\x00"...)

// probeA2S sends a Source engine A2S_INFO query over UDP, answering server
// challenges, and requires a well-formed info reply.
func probeA2S(ctx context.Context, config monitor.Config) Outcome {
	ctx, cancel := context.WithTimeout(ctx, config.ProbeTimeout())
	defer cancel()
	ips, err := net.DefaultResolver.LookupHost(ctx, config.Target)
	if err != nil {
		return outcomeOf(-1, err)
	}
	if len(ips) == 0 {
		return outcomeOf(-1, errors.New("no addresses resolved"))
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "udp", net.JoinHostPort(ips[0], strconv.Itoa(int(config.Port))))
	if err != nil {
		return outcomeOf(-1, err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	start := time.Now()
	query := a2sInfoQuery
	buf := make([]byte, maxA2SPacket)
	for challenges := 0; ; {
		if _, err := conn.Write(query); err != nil {
			return outcomeOf(-1, a2sErr(ctx, err))
		}
		n, err := conn.Read(buf)
		if err != nil {
			return outcomeOf(-1, a2sErr(ctx, err))
		}
		reply := buf[:n]
		if len(reply) >= 4 && bytes.Equal(reply[:4], []byte{0xfe, 0xff, 0xff, 0xff}) {
			return outcomeOf(-1, errA2SSplit)
		}
		if len(reply) < 5 || !bytes.Equal(reply[:4], []byte{0xff, 0xff, 0xff, 0xff}) {
			return outcomeOf(-1, errA2SInvalid)
		}
		switch reply[4] {
		case 'A':
			if len(reply) < 9 || challenges >= a2sMaxChallenges {
				return outcomeOf(-1, errA2SInvalid)
			}
			challenges++
			query = append(append([]byte(nil), a2sInfoQuery...), reply[5:9]...)
			continue
		case 'I':
			// protocol byte, then name, map, folder and game strings
			if len(reply) < 6 || !hasCStrings(reply[6:], 4) {
				return outcomeOf(-1, errA2SInvalid)
			}
		case 'm':
			// GoldSource: address, name, map, folder and game strings
			if !hasCStrings(reply[5:], 5) {
				return outcomeOf(-1, errA2SInvalid)
			}
		default:
			return outcomeOf(-1, errA2SInvalid)
		}
		return Outcome{ResponseUs: time.Since(start).Microseconds()}
	}
}

// hasCStrings reports whether data starts with count null-terminated strings.
func hasCStrings(data []byte, count int) bool {
	for range count {
		end := bytes.IndexByte(data, 0)
		if end < 0 {
			return false
		}
		data = data[end+1:]
	}
	return true
}

func a2sErr(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errProbeTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return errProbeTimeout
	}
	return err
}
