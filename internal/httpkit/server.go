package httpkit

import "time"

// IdleTimeout is how long a listener keeps a keep-alive connection open
// with no request on it: every http.Server sets it (and a
// ReadHeaderTimeout), so connections a client opened and left don't pile
// up. Longer than the two minutes Caddy keeps its idle upstream
// connections, so Caddy closes them first and never sends a request on
// one the server is closing. It doesn't limit a request in flight, so
// streams (SSE, logs, uploads, image layers) are unaffected.
const IdleTimeout = 3 * time.Minute
