package providers

import (
	"bufio"
	"bytes"
	"io"
)

// SSEEvent is one server-sent event.
type SSEEvent struct {
	Event string
	Data  []byte
}

// SSEReader parses an SSE stream incrementally.
type SSEReader struct {
	r *bufio.Reader
}

func NewSSEReader(r io.Reader) *SSEReader {
	return &SSEReader{r: bufio.NewReaderSize(r, 64<<10)}
}

// Next returns the next event. io.EOF at end of stream.
func (s *SSEReader) Next() (*SSEEvent, error) {
	ev := &SSEEvent{}
	var data bytes.Buffer
	hasData := false
	for {
		line, err := s.r.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			if hasData || ev.Event != "" {
				ev.Data = data.Bytes()
				return ev, nil
			}
			return nil, err
		}
		line = bytes.TrimRight(line, "\r\n")
		if len(line) == 0 {
			if hasData || ev.Event != "" {
				ev.Data = data.Bytes()
				return ev, nil
			}
			if err != nil {
				return nil, err
			}
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value := line, []byte(nil)
		if i := bytes.IndexByte(line, ':'); i >= 0 {
			field, value = line[:i], line[i+1:]
			if len(value) > 0 && value[0] == ' ' {
				value = value[1:]
			}
		}
		switch string(field) {
		case "event":
			ev.Event = string(value)
		case "data":
			if hasData {
				data.WriteByte('\n')
			}
			data.Write(value)
			hasData = true
		}
		if err != nil {
			ev.Data = data.Bytes()
			return ev, nil
		}
	}
}

// WriteSSE serialises an event.
func WriteSSE(w io.Writer, ev SSEEvent) error {
	var b bytes.Buffer
	if ev.Event != "" {
		b.WriteString("event: ")
		b.WriteString(ev.Event)
		b.WriteByte('\n')
	}
	b.WriteString("data: ")
	b.Write(ev.Data)
	b.WriteString("\n\n")
	_, err := w.Write(b.Bytes())
	return err
}
