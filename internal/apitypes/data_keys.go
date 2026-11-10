package apitypes

// Key-value databases in the Data panel (Redis and Valkey, ADR 0072):
// the keyspace, keys found by SCAN, and one key's value as grid rows.

// Limits of key pages.
const (
	// DataKeysLimit is the default keys per page; DataKeysMaxLimit the
	// most a request may ask for.
	DataKeysLimit    = 100
	DataKeysMaxLimit = 500
	// DataKeyValueLimit is the default items of a key's value per page
	// (fields, elements, members, entries); DataKeyValueMaxLimit the most.
	DataKeyValueLimit    = 100
	DataKeyValueMaxLimit = 1000
)

// DataKeyspaceDB is one logical database of a key-value server.
type DataKeyspaceDB struct {
	DB   int   `json:"db"`
	Keys int64 `json:"keys"`
	// Expires is how many of its keys have a TTL.
	Expires int64 `json:"expires"`
}

// DataKeysRequest finds keys with SCAN (GET /data/keys). Over HTTP every
// field is a query parameter.
type DataKeysRequest struct {
	DB int `json:"db,omitempty"`
	// Match is a SCAN glob ("user:*"); empty matches every key.
	Match string `json:"match,omitempty"`
	// Type keeps keys of one type: string, hash, list, set, zset, stream.
	Type string `json:"type,omitempty"`
	// After is the Next cursor of the previous page (same db, match and
	// type).
	After string `json:"after,omitempty"`
	// Limit is keys per page: 0 means DataKeysLimit, at most
	// DataKeysMaxLimit. A page may hold fewer (SCAN visits the keyspace in
	// bounded steps) and still have a Next.
	Limit int `json:"limit,omitempty"`
}

// DataKeyInfo is one key.
type DataKeyInfo struct {
	// Key is the key's name. Names that aren't UTF-8 come as hex with
	// Hex set, and are sent back that way.
	Key  string `json:"key"`
	Hex  bool   `json:"hex,omitempty"`
	Type string `json:"type"`
	// TTLMs is the time left in milliseconds, -1 without an expiry.
	TTLMs int64 `json:"ttl_ms"`
	// Length is its string length, fields, elements, members or entries.
	Length int64 `json:"length"`
	// Bytes is MEMORY USAGE with 5 samples: an estimate for big values.
	Bytes int64 `json:"bytes"`
	// Masked: the key matches a sensitive pattern the caller sees masked;
	// Key is then the pattern, not the name.
	Masked bool `json:"masked,omitempty"`
}

// DataKeyPage is one page of keys.
type DataKeyPage struct {
	Keys []DataKeyInfo `json:"keys"`
	// Next continues the scan; empty when the keyspace was walked.
	Next string `json:"next,omitempty"`
	// Total is the database's key count (DBSIZE).
	Total int64 `json:"total"`
}

// DataKeyRequest reads one key's value (GET /data/keys/value), a page
// at a time for collections.
type DataKeyRequest struct {
	DB  int    `json:"db,omitempty"`
	Key string `json:"key"`
	// Hex means Key is hex (DataKeyInfo.Hex).
	Hex bool `json:"hex,omitempty"`
	// After is the Next cursor of the previous page of this key.
	After string `json:"after,omitempty"`
	// Limit is items per page: 0 means DataKeyValueLimit, at most
	// DataKeyValueMaxLimit.
	Limit int `json:"limit,omitempty"`
	// Full returns a string value (or each item) untruncated, up to
	// 1 MB.
	Full bool `json:"full,omitempty"`
	// Reveal reads a key matching a sensitive pattern. Only for callers
	// with sensitive visible (others get forbidden); audited as
	// data.reveal, at most every 10 min per caller and pattern.
	Reveal bool `json:"reveal,omitempty"`
}

// SetSensitiveKeysRequest replaces a key-value database's sensitive key
// patterns (PUT /data/sensitive-keys): Redis globs such as session:*.
// Admin.
type SetSensitiveKeysRequest struct {
	Patterns []string `json:"patterns" maxItems:"200"`
}

// DataKeyValue is one key with a page of its value, as rows of a grid:
// a string is one row [value]; a hash [field, value]; a list [index,
// value]; a set [member]; a sorted set [member, score]; a stream [id,
// fields as JSON text]. Cells follow DataRow.
type DataKeyValue struct {
	DataKeyInfo
	// Encoding is the server's internal encoding (listpack, hashtable,
	// ...).
	Encoding string             `json:"encoding,omitempty"`
	Columns  []DataResultColumn `json:"columns"`
	Rows     []DataRow          `json:"rows"`
	// Next is the cursor of the following page; empty on the last one.
	Next string `json:"next,omitempty"`
}
