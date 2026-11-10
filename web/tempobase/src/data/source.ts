import type { DataSource } from "../../../src/features/data/api";
import { tb } from "../api/backend";
import { subjectNames } from "./policy";
import { keys } from "./queries";

// tempoBase's side of the shared data panel: a DataSource bound to one
// connection (ADR 0024 section 4).

/**
 * One connection's panel, for `<DataSourceContext value={...}>`. Make it
 * once per connection (useMemo): the panel's hooks key on it.
 */
export function connectionSource(connection: string): DataSource {
  return {
    ...tb().data(connection),
    key: keys.data(connection),
    roles: {
      reader: "tempobase_reader",
      writer: "tempobase_writer",
      masked: "tempobase_reader_masked",
    },
    confirmName: connection,
    words: {
      product: "tempoBase",
      owner: "the user from the connection's URL",
      target: "Connection",
      unreachable:
        "The database didn't answer. Check the URL and that tempoBase can reach the host.",
      notRunning: "The database isn't running.",
      queryStatsOff:
        "The server doesn't load pg_stat_statements. Turn it on in your provider's settings, or add it to shared_preload_libraries and restart the server; then an admin creates the extension here.",
      stagesQueryStats: false,
    },
    subjectNames,
    policy: {
      queryKey: keys.policy(connection),
      queryFn: () => tb().policy(connection),
    },
  };
}
