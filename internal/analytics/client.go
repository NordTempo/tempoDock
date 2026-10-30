package analytics

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var (
	// ErrOffline means the store didn't answer: its container isn't
	// running, or the analytics server is offline.
	ErrOffline = errors.New("the analytics store is offline")
	// ErrBusy means the store is up but its queries were all taken for
	// longer than a query may wait; asking again later works.
	ErrBusy = errors.New("the analytics store is busy; try again in a moment")
)

// Client is Store over the dock-analytics container's HTTP API.
type Client struct {
	// BaseURL is the container's address, e.g. http://127.0.0.1:7076.
	BaseURL string
	// Token is the install's analytics token, sent on every call.
	Token string
	// HTTP makes the requests; nil is http.DefaultClient. A store on
	// another server gets one that dials over the node link.
	HTTP *http.Client
}

var _ Store = (*Client)(nil)

// Write implements Store: the events go as JSON lines.
func (c *Client) Write(ctx context.Context, source string, position uint64, events []Event) (int, error) {
	var body bytes.Buffer
	for i := range events {
		if err := json.MarshalWrite(&body, &events[i]); err != nil {
			return 0, err
		}
		body.WriteByte('\n')
	}
	q := url.Values{"source": {source}, "position": {strconv.FormatUint(position, 10)}}
	var res WriteResult
	err := c.do(ctx, http.MethodPost, WritePath+"?"+q.Encode(), &body, &res)
	return res.Written, err
}

// WriteLines is Write for events that are already JSON lines, as a
// spool holds them: they go to the store as they are. The store skips
// lines it can't read.
func (c *Client) WriteLines(ctx context.Context, source string, position uint64, lines []byte) (int, error) {
	q := url.Values{"source": {source}, "position": {strconv.FormatUint(position, 10)}}
	var res WriteResult
	err := c.do(ctx, http.MethodPost, WritePath+"?"+q.Encode(), bytes.NewReader(lines), &res)
	return res.Written, err
}

// Cursor implements Store.
func (c *Client) Cursor(ctx context.Context, source string) (uint64, error) {
	var res CursorResult
	err := c.do(ctx, http.MethodGet, CursorPath+"?"+url.Values{"source": {source}}.Encode(), nil, &res)
	return res.Position, err
}

// Overview implements Store.
func (c *Client) Overview(ctx context.Context, q Query) (Overview, error) {
	var res Overview
	return res, c.post(ctx, OverviewPath, q, &res)
}

// Series implements Store.
func (c *Client) Series(ctx context.Context, q SeriesQuery) ([]Point, error) {
	var res []Point
	return res, c.post(ctx, SeriesPath, q, &res)
}

// Breakdown implements Store.
func (c *Client) Breakdown(ctx context.Context, q BreakdownQuery) ([]Row, error) {
	var res []Row
	return res, c.post(ctx, BreakdownPath, q, &res)
}

// Health implements Store.
func (c *Client) Health(ctx context.Context) (Health, error) {
	var res Health
	return res, c.do(ctx, http.MethodGet, HealthPath, nil, &res)
}

// post sends in as JSON and decodes the answer into out.
func (c *Client) post(ctx context.Context, path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, path, bytes.NewReader(b), out)
}

// maxAnswer caps an answer from the store.
const maxAnswer = 8 << 20

// do makes one call. A store that can't be reached is ErrOffline
// (wrapped), a busy one ErrBusy; one that answers with an error returns
// its message as a StoreError.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %w", ErrOffline, err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := io.LimitReader(resp.Body, maxAnswer)
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(r, 4<<10))
		switch resp.StatusCode {
		case http.StatusServiceUnavailable:
			return fmt.Errorf("%w: %s", ErrOffline, strings.TrimSpace(string(msg)))
		case http.StatusTooManyRequests:
			return ErrBusy
		}
		return &StoreError{Status: resp.StatusCode, Message: strings.TrimSpace(string(msg))}
	}
	return json.UnmarshalRead(r, out)
}

// StoreError is an error the store answered with: a bad query (400) or
// its own failure (500).
type StoreError struct {
	Status  int
	Message string
}

// Error implements error.
func (e *StoreError) Error() string { return "analytics store: " + e.Message }
