package apiclient

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestEventReader(t *testing.T) {
	stream := ": keep-alive\n\n" +
		"event: build\nid: 1\ndata: step 1\n\n" +
		"data: line one\r\ndata:line two\r\n\r\n" + // CRLF, no space after colon
		"event: ignored-without-data\n\n" +
		"retry: 1000\rid: 7\rdata: cr only\r\r" +
		"data: {\"n\":1}\n\n" +
		"data: partial at EOF"

	r := NewEventReader(io.NopCloser(strings.NewReader(stream)))
	want := []Event{
		{ID: "1", Type: "build", Data: "step 1"},
		{ID: "1", Type: "message", Data: "line one\nline two"},
		{ID: "7", Type: "message", Data: "cr only"},
		{ID: "7", Type: "message", Data: `{"n":1}`},
	}
	for i, w := range want {
		got, err := r.Next()
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if got != w {
			t.Errorf("event %d = %+v, want %+v", i, got, w)
		}
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("want io.EOF after the last complete event, got %v", err)
	}
}
