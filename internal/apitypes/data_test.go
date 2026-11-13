package apitypes

import "testing"

// A server is named by its engine, unless its version names it already.
func TestServerLabel(t *testing.T) {
	for _, tc := range []struct {
		engine        DataEngine
		version, want string
	}{
		{EnginePostgres, "17.2", "Postgres 17.2"},
		{EngineMySQL, "8.4.13", "MySQL 8.4.13"},
		{EngineMySQL, "MariaDB 11.4.5", "MariaDB 11.4.5"},
		{EngineRedis, "Redis 8.10.2", "Redis 8.10.2"},
		{EngineRedis, "Valkey 8.1.1", "Valkey 8.1.1"},
		{EngineMySQL, "", "MySQL"},
	} {
		if got := ServerLabel(tc.engine, tc.version); got != tc.want {
			t.Errorf("ServerLabel(%s, %q) = %q, want %q", tc.engine, tc.version, got, tc.want)
		}
	}
}
