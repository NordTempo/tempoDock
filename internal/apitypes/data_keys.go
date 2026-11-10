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

// DataKeyOp is what one change does to a key.
type DataKeyOp string

// Key change operations: set a string (keeping its TTL), delete a key,
// set or remove its TTL, set or remove a hash field, add or remove a set
// or sorted set member, set a list element by index, push to a list's
// end (rpush) or front (lpush), remove an element's first occurrence
// (lrem).
const (
	KeySet          DataKeyOp = "set"
	KeyDelete       DataKeyOp = "delete"
	KeyExpire       DataKeyOp = "expire"
	KeyHashSet      DataKeyOp = "hset"
	KeyHashDelete   DataKeyOp = "hdel"
	KeySetAdd       DataKeyOp = "sadd"
	KeySetRemove    DataKeyOp = "srem"
	KeyZAdd         DataKeyOp = "zadd"
	KeyZRemove      DataKeyOp = "zrem"
	KeyListSet      DataKeyOp = "lset"
	KeyListPush     DataKeyOp = "rpush"
	KeyListPushLeft DataKeyOp = "lpush"
	KeyListRemove   DataKeyOp = "lrem"
)

// DataKeyChange is one change to one key of a Redis database, in a
// changeset (DataChangesetRequest.KeyChanges). Every change but a push
// carries Old, what the editor saw; a key or field that changed since
// fails the whole changeset with a conflict.
type DataKeyChange struct {
	Op  DataKeyOp `json:"op" enum:"set,delete,expire,hset,hdel,sadd,srem,zadd,zrem,lset,rpush,lpush,lrem"`
	DB  int       `json:"db,omitempty"`
	Key string    `json:"key"`
	// Field is the hash field (hset, hdel), the member (sadd, srem, zadd,
	// zrem), the list index (lset) or the element to remove (lrem).
	Field *string `json:"field,omitempty"`
	// Value is the string (set), the field's value (hset), the element
	// (lset, rpush, lpush) or the score (zadd).
	Value *string `json:"value,omitempty"`
	// TTLMs is expire's time to live in milliseconds; -1 removes it.
	TTLMs *int64      `json:"ttl_ms,omitempty"`
	Old   *DataKeyOld `json:"old,omitempty"`
}

// DataKeyOld is what the editor saw of a key: its type ("none" when it
// didn't exist) and the value the change replaces or removes: the string
// (set), the field's value (hset, hdel), the score (zadd, zrem), the
// element (lset), "1" for a set member that was there (sadd, srem);
// null when it was absent.
type DataKeyOld struct {
	Type  string  `json:"type"`
	Value *string `json:"value,omitempty"`
}
