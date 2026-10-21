package apiclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Event is one Server-Sent Event.
type Event struct {
	// ID is the last event ID seen on the stream (it carries over to later
	// events, per the spec).
	ID string
	// Type is the event field, "message" when the server sent none.
	Type string
	// Data is the data lines joined with "\n".
	Data string
}

// EventReader parses a text/event-stream body. It does not reconnect.
type EventReader struct {
	body   io.ReadCloser
	sc     *bufio.Scanner
	lastID string
}

// maxLine bounds one line of the stream (a log line can be long).
const maxLine = 1 << 20

// NewEventReader reads events from body; Close closes body.
func NewEventReader(body io.ReadCloser) *EventReader {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 4096), maxLine)
	sc.Split(scanLines)
	return &EventReader{body: body, sc: sc}
}

// Next returns the next event. It returns io.EOF when the stream ends; a
// partial event at the end is discarded, as the spec requires.
func (r *EventReader) Next() (Event, error) {
	var typ string
	var data strings.Builder
	hasData := false
	for r.sc.Scan() {
		line := r.sc.Text()
		if line == "" { // blank line dispatches
			if !hasData {
				typ = ""
				continue
			}
			if typ == "" {
				typ = "message"
			}
			return Event{ID: r.lastID, Type: typ, Data: strings.TrimSuffix(data.String(), "\n")}, nil
		}
		if line[0] == ':' {
			continue // comment, e.g. a keep-alive
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			typ = value
		case "data":
			data.WriteString(value)
			data.WriteByte('\n')
			hasData = true
		case "id":
			if !strings.ContainsRune(value, 0) {
				r.lastID = value
			}
		}
		// "retry" and unknown fields are ignored: we don't reconnect.
	}
	if err := r.sc.Err(); err != nil {
		return Event{}, err
	}
	return Event{}, io.EOF
}

// Close closes the underlying stream.
func (r *EventReader) Close() error { return r.body.Close() }

// scanLines splits on "\r\n", "\n" or a lone "\r", per the SSE spec.
func scanLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		if data[i] == '\n' {
			return i + 1, data[:i], nil
		}
		// "\r": need one more byte to tell "\r\n" from a lone "\r".
		if i+1 < len(data) {
			if data[i+1] == '\n' {
				return i + 2, data[:i], nil
			}
			return i + 1, data[:i], nil
		}
		if atEOF {
			return i + 1, data[:i], nil
		}
		return 0, nil, nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// Each reads r until the stream's `end` event, decoding every other event's
// data into a T and passing it to fn. The server's `error` event becomes an
// exit-coded error, like a problem response. A stream that stops without
// `end` returns io.ErrUnexpectedEOF.
func Each[T any](r *EventReader, fn func(T) error) error {
	for {
		ev, err := r.Next()
		if errors.Is(err, io.EOF) {
			return io.ErrUnexpectedEOF
		}
		if err != nil {
			return err
		}
		switch ev.Type {
		case "end":
			return nil
		case "error":
			p := &Problem{}
			if err := json.Unmarshal([]byte(ev.Data), p); err != nil {
				return fmt.Errorf("stream failed: %s", ev.Data)
			}
			return problemExit(p, p.Status)
		}
		var v T
		if err := json.Unmarshal([]byte(ev.Data), &v); err != nil {
			return fmt.Errorf("decode %s event: %w", ev.Type, err)
		}
		if err := fn(v); err != nil {
			return err
		}
	}
}

// Follow opens the event stream at path and passes its events to fn until
// the stream ends (see Each).
func Follow[T any](ctx context.Context, c *Client, path string, fn func(T) error) error {
	r, err := c.Events(ctx, path)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	err = Each(r, fn)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
