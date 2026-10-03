package providers

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
)

// esMessage is one decoded AWS event-stream frame.
type esMessage struct {
	Headers map[string]string
	Payload []byte
}

// readEventStream decodes a single application/vnd.amazon.eventstream frame.
func readEventStream(r *bufio.Reader) (*esMessage, error) {
	var prelude [12]byte
	if _, err := io.ReadFull(r, prelude[:]); err != nil {
		return nil, err
	}
	total := binary.BigEndian.Uint32(prelude[0:4])
	hlen := binary.BigEndian.Uint32(prelude[4:8])
	if crc32.ChecksumIEEE(prelude[:8]) != binary.BigEndian.Uint32(prelude[8:12]) {
		return nil, errors.New("eventstream: prelude crc mismatch")
	}
	if total < 16 || total > 16<<20 || hlen > total-16 {
		return nil, fmt.Errorf("eventstream: bad lengths total=%d headers=%d", total, hlen)
	}
	rest := make([]byte, total-12)
	if _, err := io.ReadFull(r, rest); err != nil {
		return nil, err
	}
	crc := crc32.NewIEEE()
	crc.Write(prelude[:])
	crc.Write(rest[:len(rest)-4])
	if crc.Sum32() != binary.BigEndian.Uint32(rest[len(rest)-4:]) {
		return nil, errors.New("eventstream: message crc mismatch")
	}
	m := &esMessage{Headers: map[string]string{}}
	h := rest[:hlen]
	for len(h) > 0 {
		nl := int(h[0])
		if len(h) < 1+nl+1 {
			return nil, errors.New("eventstream: truncated header")
		}
		name := string(h[1 : 1+nl])
		typ := h[1+nl]
		h = h[2+nl:]
		var val string
		switch typ {
		case 0:
			val = "true"
		case 1:
			val = "false"
		case 2:
			val, h = fmt.Sprint(int8(h[0])), h[1:]
		case 3:
			val, h = fmt.Sprint(int16(binary.BigEndian.Uint16(h))), h[2:]
		case 4:
			val, h = fmt.Sprint(int32(binary.BigEndian.Uint32(h))), h[4:]
		case 5, 8:
			val, h = fmt.Sprint(int64(binary.BigEndian.Uint64(h))), h[8:]
		case 6, 7:
			l := int(binary.BigEndian.Uint16(h))
			val, h = string(h[2:2+l]), h[2+l:]
		case 9:
			val, h = fmt.Sprintf("%x", h[:16]), h[16:]
		default:
			return nil, fmt.Errorf("eventstream: unknown header type %d", typ)
		}
		m.Headers[name] = val
	}
	m.Payload = rest[hlen : len(rest)-4]
	return m, nil
}

// EncodeEventStream builds a frame (used by tests and the mock provider).
func EncodeEventStream(headers map[string]string, payload []byte) []byte {
	var hb []byte
	for k, v := range headers {
		hb = append(hb, byte(len(k)))
		hb = append(hb, k...)
		hb = append(hb, 7)
		hb = binary.BigEndian.AppendUint16(hb, uint16(len(v)))
		hb = append(hb, v...)
	}
	total := 12 + len(hb) + len(payload) + 4
	out := make([]byte, 0, total)
	out = binary.BigEndian.AppendUint32(out, uint32(total))
	out = binary.BigEndian.AppendUint32(out, uint32(len(hb)))
	out = binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(out[:8]))
	out = append(out, hb...)
	out = append(out, payload...)
	out = binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(out))
	return out
}

// bedrockStreamToSSE converts Bedrock InvokeModelWithResponseStream output into
// Anthropic-style SSE so the rest of the gateway treats all Claude providers alike.
func bedrockStreamToSSE(body io.ReadCloser) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer body.Close()
		br := bufio.NewReaderSize(body, 64<<10)
		for {
			m, err := readEventStream(br)
			if err != nil {
				if errors.Is(err, io.EOF) {
					pw.Close()
				} else {
					pw.CloseWithError(err)
				}
				return
			}
			if mt := m.Headers[":message-type"]; mt == "exception" || mt == "error" {
				et := m.Headers[":exception-type"]
				if et == "" {
					et = m.Headers[":error-code"]
				}
				var em struct {
					Message string `json:"message"`
				}
				_ = json.Unmarshal(m.Payload, &em)
				if em.Message == "" {
					em.Message = m.Headers[":error-message"]
				}
				typ := "api_error"
				switch {
				case strings.Contains(strings.ToLower(et), "throttl"), strings.Contains(strings.ToLower(et), "servicequota"):
					typ = "rate_limit_error"
				case strings.Contains(strings.ToLower(et), "overloaded"), strings.Contains(strings.ToLower(et), "unavailable"):
					typ = "overloaded_error"
				case strings.Contains(strings.ToLower(et), "validation"):
					typ = "invalid_request_error"
				}
				data := marshal(map[string]any{"type": "error", "error": map[string]any{"type": typ, "message": et + ": " + em.Message}})
				fmt.Fprintf(pw, "event: error\ndata: %s\n\n", data)
				pw.Close()
				return
			}
			var chunk struct {
				Bytes string `json:"bytes"`
			}
			if err := json.Unmarshal(m.Payload, &chunk); err != nil || chunk.Bytes == "" {
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(chunk.Bytes)
			if err != nil {
				continue
			}
			var t struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(raw, &t)
			if _, err := fmt.Fprintf(pw, "event: %s\ndata: %s\n\n", t.Type, raw); err != nil {
				return
			}
		}
	}()
	return pr
}
