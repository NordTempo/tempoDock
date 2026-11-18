package data

import (
	"errors"
	"net"
	"sync/atomic"
)

// ErrReadBudget is what a MeteredConn's Read fails with once the server
// sent more than the budget in answer to one request. The connection is
// unusable after it: close it.
var ErrReadBudget = errors.New("the database sent more than the Data panel reads for one request")

// MeteredConn is a connection to a database that counts what it reads:
// the engines' own caps (a row, a reply, a page) bound what they keep of
// a result, not what a server may send beside it. A Read past budget
// bytes since the last Write fails with ErrReadBudget, and Total lets an
// engine stop reusing a connection that has read its share. Counting is
// two atomic adds a Read and one store a Write (the drivers read and
// write from different goroutines), no allocation.
type MeteredConn struct {
	net.Conn
	budget int64
	// since is what was read since the last Write (one request's answer),
	// total since the connection opened.
	since, total atomic.Int64
}

// Meter wraps nc; one request's answer may be budget bytes.
func Meter(nc net.Conn, budget int64) *MeteredConn {
	return &MeteredConn{Conn: nc, budget: budget}
}

// Read reads from the connection and counts the bytes, or fails with
// ErrReadBudget when the answer to the last request is past the budget.
func (m *MeteredConn) Read(p []byte) (int, error) {
	if m.since.Load() > m.budget {
		return 0, ErrReadBudget
	}
	n, err := m.Conn.Read(p)
	m.since.Add(int64(n))
	m.total.Add(int64(n))
	return n, err
}

// Write sends a request, which starts a new answer's count.
func (m *MeteredConn) Write(p []byte) (int, error) {
	m.since.Store(0)
	return m.Conn.Write(p)
}

// Total is how many bytes the connection has read since it opened.
func (m *MeteredConn) Total() int64 { return m.total.Load() }
