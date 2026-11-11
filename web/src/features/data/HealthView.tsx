import { useMutation } from "@tanstack/react-query";
import {
  type ReactNode,
  useEffect,
  useEffectEvent,
  useRef,
  useState,
} from "react";
import { useShortcuts } from "../../shell/keys";
import { clockSec, elapsed } from "../../ui/format";
import { useCli, useDataSource, useInsights } from "./api";
import { byteSize } from "./cells";
import { type DataEngine, engines } from "./engines";
import { Act, Btn, CopyBtn, ErrorText, Kbd, Scroll } from "./kit";
import { ConfirmName } from "./ReviewPanel";
import type {
  DataHint,
  DataIndexStats,
  DataInsights,
  DataInsightView,
  DataKeyPrefix,
  DataOverview,
  DataPermission,
  DataSession,
  DataStatement,
  DataStatementOrder,
  DataTableStats,
} from "./types";

// The Health tab (ADR 0069, mock B): how the database is doing, read live
// from its own statistics when the tab opens and on Refresh (R). Views
// are the API's: an overview with what needs a look, then one dense list
// each for queries, sessions, tables and indexes, with details on the
// side; Redis has commands, clients and key prefixes instead (engines.ts).
// Nothing refreshes by itself except Follow on Sessions, a timer
// that runs only while the tab is visible and stops after followFor.

/** How often Follow refreshes the sessions, and for how long at most. */
const followEvery = 5_000;
const followFor = 10 * 60_000;

const viewLabels: Record<DataInsightView, string> = {
  overview: "Overview",
  queries: "Queries",
  sessions: "Sessions",
  tables: "Tables",
  indexes: "Indexes",
  keys: "Keys",
};

const orders: { order: DataStatementOrder; label: string }[] = [
  { order: "total", label: "total" },
  { order: "mean", label: "mean" },
  { order: "calls", label: "calls" },
  { order: "rows", label: "rows" },
];

export default function HealthView({
  engine,
  access,
  view: asked,
  onView,
  onExplain,
}: {
  engine: DataEngine;
  access: DataPermission;
  view: DataInsightView;
  onView: (view: DataInsightView) => void;
  /** Open SQL in the SQL tab. */
  onExplain: (sql: string) => void;
}) {
  const { health: views, healthLabels } = engines[engine];
  // A view the engine doesn't have (a link from another database) is
  // the overview.
  const view = views.includes(asked) ? asked : "overview";
  const [order, setOrder] = useState<DataStatementOrder>("total");
  const insights = useInsights(order);
  const [selected, setSelected] = useState<string | null>(null);
  const cli = useCli();
  const refresh = () => void insights.refetch();
  useShortcuts([
    { key: "r", label: "refresh", run: refresh },
    ...(selected
      ? [{ key: "escape", label: "close", run: () => setSelected(null) }]
      : []),
  ]);

  // The read before the current one, for the queries view's changes.
  const previous = usePrevious(insights.data);

  const open = (v: DataInsightView, subject?: string) => {
    onView(v);
    setSelected(subject ?? null);
  };

  const ins = insights.data;
  const counts = ins && viewCounts(ins);
  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <div className="flex min-h-10 flex-wrap items-center gap-x-5 gap-y-1 border-b border-(--g-line) px-3 py-1.5 text-xs text-(--g-dim)">
        {ins?.overview && <Facts ins={ins} />}
        <span className="ml-auto flex items-center gap-2">
          {ins && (
            <span className="text-(--g-mut)">read {clockSec(ins.read_at)}</span>
          )}
          <ErrorText error={insights.error} />
          <Act
            small
            cli={cli("dock db health")}
            disabled={insights.isFetching}
            onClick={refresh}
          >
            {insights.isFetching ? "Reading" : "Refresh"} <Kbd>R</Kbd>
          </Act>
        </span>
      </div>
      <nav
        aria-label="Health"
        className="g-tabs border-b border-(--g-line) px-3 py-1.5"
      >
        {views.map((v) => (
          <button
            key={v}
            type="button"
            aria-pressed={view === v}
            onClick={() => open(v)}
          >
            {healthLabels?.[v] ?? viewLabels[v]}
            {!!counts?.[v] && (
              <span className="text-(--g-warn)">{counts[v]}</span>
            )}
          </button>
        ))}
      </nav>
      {!ins ? (
        <p className="p-5 text-(--g-mut)">
          {insights.error ? "" : "Reading the database's statistics"}
        </p>
      ) : (
        <>
          {(ins.unavailable ?? [])
            .filter(
              (u) =>
                u.view === view &&
                // QueryStatsOff says it, with what to do.
                !(
                  view === "queries" &&
                  ins.overview &&
                  ins.overview.query_stats !== "on" &&
                  !u.partial
                ),
            )
            .map((u) => (
              <p
                key={u.reason}
                className="border-b border-(--g-line) px-3 py-2 text-xs text-(--g-warn-ink)"
              >
                {u.partial ? "Partly shown: " : "Not shown: "}
                {u.reason}
              </p>
            ))}
          {view === "overview" ? (
            <Overview ins={ins} onOpen={open} />
          ) : view === "queries" ? (
            <Queries
              ins={ins}
              previous={
                previous?.read_at !== ins.read_at ? previous : undefined
              }
              order={order}
              onOrder={setOrder}
              selected={selected}
              onSelect={setSelected}
              onExplain={onExplain}
              canRead={access.level !== "schema"}
              access={access}
              onDone={refresh}
            />
          ) : view === "sessions" ? (
            <Sessions
              ins={ins}
              selected={selected}
              onSelect={setSelected}
              onRefresh={refresh}
              access={access}
            />
          ) : view === "tables" ? (
            <Tables ins={ins} onOpen={open} />
          ) : view === "keys" ? (
            <KeyPrefixes ins={ins} />
          ) : (
            <Indexes ins={ins} />
          )}
        </>
      )}
    </div>
  );
}

/** The value before the latest one. */
function usePrevious<T>(value: T | undefined) {
  const ref = useRef<{ current?: T; previous?: T }>({});
  if (value !== ref.current.current) {
    ref.current = { previous: ref.current.current, current: value };
  }
  return ref.current.previous;
}

/** Warnings per view, and sessions not idle, for the view buttons. */
function viewCounts(
  ins: DataInsights,
): Partial<Record<DataInsightView, number>> {
  const out: Partial<Record<DataInsightView, number>> = {
    overview: (ins.hints ?? []).filter((h) => h.level === "warn").length,
  };
  for (const h of ins.hints ?? []) {
    if (h.view !== "overview" && h.view !== "sessions")
      out[h.view] = (out[h.view] ?? 0) + 1;
  }
  out.sessions = (ins.sessions ?? []).filter(
    (s) => s.kind === "client" && s.state !== "idle",
  ).length;
  return out;
}

/** The one-line facts over every view. */
function Facts({ ins }: { ins: DataInsights }) {
  const ov = ins.overview;
  if (!ov) return null;
  const p = ov.connections;
  const usable = p.max - p.reserved;
  const near = p.full || p.used >= 0.8 * usable;
  const redis = ins.engine === "redis";
  return (
    <>
      <span>
        <span
          className={`font-mono ${near ? "text-(--g-warn)" : "text-(--g-text)"}`}
        >
          {p.full ? "full" : `${p.used} / ${usable}`}
        </span>{" "}
        connections
      </span>
      <span>
        <span className="font-mono text-(--g-text)">
          {percent(ov.cache_hit)}
        </span>{" "}
        {redis ? "hits" : "cache"}
      </span>
      <span>
        <span className="font-mono text-(--g-text)">{byteSize(ov.bytes)}</span>
        {redis && (ov.max_bytes ? ` of ${byteSize(ov.max_bytes)}` : " memory")}
      </span>
      {ins.since && (
        <span className="text-(--g-mut)">counting since {sinceWords(ins)}</span>
      )}
    </>
  );
}

/** "3d 4h ago (server start)" */
function sinceWords(ins: DataInsights) {
  if (!ins.since) return "";
  const d = elapsedLong(Date.parse(ins.read_at) - Date.parse(ins.since));
  return `${d} ago (${ins.since_reason === "reset" ? "stats reset" : "server start"})`;
}

/** A duration with days: "3d 4h", else elapsed's "1m 12s". */
function elapsedLong(ms: number) {
  const h = Math.floor(ms / 3_600_000);
  if (h < 24) return elapsed(ms);
  return `${Math.floor(h / 24)}d ${h % 24}h`;
}

const percent = (r: number | undefined) =>
  r === undefined ? "-" : `${(r * 100).toFixed(1)}%`;

const count = (n: number) => n.toLocaleString("en");

/** A statement time in ms: "0.32 ms", "41 ms", "2.05 s", "41m 8s". */
function ms(n: number) {
  if (n < 1) return `${n.toFixed(2)} ms`;
  if (n < 1000) return `${Math.round(n)} ms`;
  if (n < 60_000) return `${(n / 1000).toFixed(2)} s`;
  return elapsed(n);
}

/** How long ago `iso` was, as of the read. */
const forAt = (ins: DataInsights, iso: string | undefined) =>
  iso ? elapsed(Date.parse(ins.read_at) - Date.parse(iso)) : "-";

/** The overview: the numbers, then what needs a look. */
function Overview({
  ins,
  onOpen,
}: {
  ins: DataInsights;
  onOpen: (view: DataInsightView, subject?: string) => void;
}) {
  const ov = ins.overview;
  const hints = ins.hints ?? [];
  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      {ov && ins.engine === "redis" ? (
        <RedisFacts ins={ins} ov={ov} />
      ) : (
        ov && (
          <dl className="flex flex-wrap gap-x-7 gap-y-2 border-b border-(--g-line) px-3 py-3">
            <Fact k="connections">
              {ov.connections.full
                ? "every one taken"
                : `${ov.connections.used} of ${ov.connections.max - ov.connections.reserved} usable`}
            </Fact>
            <Fact
              k="idle in transaction"
              warn={ov.connections.idle_in_transaction > 0}
            >
              {ov.connections.idle_in_transaction}
            </Fact>
            <Fact k="waiting on locks" warn={ov.connections.waiting > 0}>
              {ov.connections.waiting}
            </Fact>
            <Fact k="oldest transaction">
              {forAt(ins, ov.connections.oldest_transaction_at)}
            </Fact>
            <Fact k="cache hit">{percent(ov.cache_hit)}</Fact>
            <Fact k="size">{byteSize(ov.bytes)}</Fact>
            {ins.engine === "postgres" && (
              <Fact k="spilled to disk">{byteSize(ov.temp_bytes)}</Fact>
            )}
            <Fact k="deadlocks">{ov.deadlocks}</Fact>
            {ov.transaction_id_age !== undefined && (
              <Fact k="transaction ID age">
                {`${Math.round(ov.transaction_id_age / 1e6)}M`}
              </Fact>
            )}
          </dl>
        )
      )}
      <h2 className="px-3 pt-3 pb-1 text-xs font-semibold text-(--g-text)">
        Needs a look{" "}
        <span className="font-normal text-(--g-mut)">{hints.length}</span>
      </h2>
      {hints.length === 0 ? (
        <p className="px-3 py-2 text-(--g-mut)">Nothing right now.</p>
      ) : (
        <Scroll>
          <table className="g-table">
            <tbody>
              {hints.map((h) => (
                <HintRow
                  key={`${h.kind}:${h.subject ?? ""}`}
                  hint={h}
                  onOpen={onOpen}
                />
              ))}
            </tbody>
          </table>
        </Scroll>
      )}
    </div>
  );
}

/** Redis' vital numbers: clients, memory against its limit, hits. */
function RedisFacts({ ins, ov }: { ins: DataInsights; ov: DataOverview }) {
  const near = !!ov.max_bytes && ov.bytes >= 0.8 * ov.max_bytes;
  return (
    <dl className="flex flex-wrap gap-x-7 gap-y-2 border-b border-(--g-line) px-3 py-3">
      <Fact k="clients">
        {ov.connections.full
          ? "every one taken"
          : `${ov.connections.used} of ${ov.connections.max}`}
      </Fact>
      <Fact k="memory" warn={near}>
        {byteSize(ov.bytes)}
        {ov.max_bytes ? ` of ${byteSize(ov.max_bytes)}` : ", no limit"}
      </Fact>
      <Fact k="at the limit">
        {ov.eviction_policy === "noeviction"
          ? "writes fail"
          : (ov.eviction_policy ?? "-")}
      </Fact>
      <Fact k="evicted keys" warn={!!ov.evicted_keys}>
        {count(ov.evicted_keys ?? 0)}
      </Fact>
      <Fact k="hit rate">{percent(ov.cache_hit)}</Fact>
      <Fact k="commands/s">{count(ov.ops_per_sec ?? 0)}</Fact>
      <Fact k="keys">{count(ov.keys ?? 0)}</Fact>
      <Fact k="up for">{forAt(ins, ov.started_at)}</Fact>
    </dl>
  );
}

function Fact({
  k,
  warn,
  children,
}: {
  k: string;
  warn?: boolean;
  children: ReactNode;
}) {
  return (
    <div className="flex flex-col">
      <dt className="text-2xs text-(--g-mut)">{k}</dt>
      <dd
        className={`font-mono text-[13px] ${warn ? "text-(--g-warn)" : "text-(--g-text)"}`}
      >
        {children}
      </dd>
    </div>
  );
}

/** One hint: its level, what, the subject and evidence, and its actions. */
function HintRow({
  hint,
  onOpen,
}: {
  hint: DataHint;
  onOpen: (view: DataInsightView, subject?: string) => void;
}) {
  return (
    <tr>
      <td
        className={`w-12 text-xs ${hint.level === "warn" ? "text-(--g-warn)" : "text-(--g-mut)"}`}
      >
        {hint.level}
      </td>
      <td className="font-medium">{hint.title}</td>
      <td className="font-mono text-xs">{subjectLabel(hint.subject)}</td>
      <td className="w-full text-xs whitespace-normal text-(--g-dim)">
        {hint.detail}
      </td>
      <td className="text-right">
        <span className="inline-flex gap-1.5">
          {hint.copy && <CopyBtn text={hint.copy} label="Copy statement" />}
          {hint.view !== "overview" && (
            <Btn small onClick={() => onOpen(hint.view, hint.subject)}>
              Show
            </Btn>
          )}
        </span>
      </td>
    </tr>
  );
}

/** A session ID is "pid.start"; people know the pid. */
const subjectLabel = (s: string | undefined) => {
  if (!s) return "";
  const m = /^(\d+)\.\d+$/.exec(s);
  return m ? `pid ${m[1]}` : s;
};

/** Statements with their changes since the previous read. */
function deltas(ins: DataInsights, previous: DataInsights) {
  const before = new Map((previous.queries ?? []).map((q) => [q.id, q]));
  return (ins.queries ?? []).flatMap((q) => {
    const b = before.get(q.id);
    const calls = q.calls - (b?.calls ?? 0);
    const total = q.total_ms - (b?.total_ms ?? 0);
    if (calls <= 0) return [];
    return [
      {
        ...q,
        calls,
        total_ms: total,
        mean_ms: total / calls,
        rows: q.rows - (b?.rows ?? 0),
      },
    ];
  });
}

function Queries({
  ins,
  previous,
  order,
  onOrder,
  selected,
  onSelect,
  onExplain,
  canRead,
  access,
  onDone,
}: {
  ins: DataInsights;
  previous: DataInsights | undefined;
  order: DataStatementOrder;
  onOrder: (o: DataStatementOrder) => void;
  selected: string | null;
  onSelect: (id: string | null) => void;
  onExplain: (sql: string) => void;
  canRead: boolean;
  access: DataPermission;
  /** Read again after an action. */
  onDone: () => void;
}) {
  const [changes, setChanges] = useState(false);
  const showChanges = changes && !!previous;
  const list =
    showChanges && previous ? deltas(ins, previous) : (ins.queries ?? []);
  const current = list.find((q) => q.id === selected);
  const all = ins.queries ?? [];
  const totalAll = all.reduce((n, q) => n + q.total_ms, 0);
  // Redis counts commands: no users, rows, cache or temp files.
  const sql = ins.engine !== "redis";
  const noun = sql ? "statements" : "commands";
  return (
    <div className="flex min-h-0 flex-1">
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 border-b border-(--g-line) px-3 py-1.5 text-xs text-(--g-mut)">
          <span className="flex items-center gap-2">
            order
            <fieldset className="g-seg" aria-label="Order">
              {orders
                .filter((o) => sql || o.order !== "rows")
                .map((o) => (
                  <button
                    key={o.order}
                    type="button"
                    aria-pressed={order === o.order}
                    onClick={() => onOrder(o.order)}
                  >
                    {o.label}
                  </button>
                ))}
            </fieldset>
          </span>
          <span className="flex items-center gap-2">
            show
            <fieldset className="g-seg" aria-label="Counts">
              <button
                type="button"
                aria-pressed={!showChanges}
                onClick={() => setChanges(false)}
              >
                {!ins.since
                  ? "totals"
                  : `since ${ins.since_reason === "reset" ? "reset" : "start"}`}
              </button>
              <button
                type="button"
                aria-pressed={showChanges}
                disabled={!previous}
                title={
                  previous ? undefined : "Refresh once to compare two reads"
                }
                onClick={() => setChanges(true)}
              >
                {previous
                  ? `since ${clockSec(previous.read_at)}`
                  : "since last refresh"}
              </button>
            </fieldset>
          </span>
          {ins.queries_tracked !== undefined && (
            <span className="ml-auto">
              {all.length} of {count(ins.queries_tracked)} {noun}
              {/* MySQL's digests aren't per user: Dock's own show too. */}
              {ins.engine === "postgres" && " · Dock's own hidden"}
            </span>
          )}
          {access.manage && ins.overview?.query_stats === "on" && (
            <ResetStats onDone={onDone} />
          )}
        </div>
        {ins.overview && ins.overview.query_stats !== "on" && (
          <QueryStatsOff
            engine={ins.engine}
            state={ins.overview.query_stats}
            access={access}
            onDone={onDone}
          />
        )}
        <div className="min-h-0 flex-1 overflow-y-auto">
          <Scroll>
            <table className="g-table">
              <thead>
                <tr>
                  <th>{sql ? "statement" : "command"}</th>
                  {sql && <th>user</th>}
                  <th className="text-right">calls</th>
                  <th className="text-right">total</th>
                  <th className="text-right">mean</th>
                  <th
                    className="text-right"
                    title={sql ? undefined : "The slowest call in the slow log"}
                  >
                    {sql ? "max" : "slowest"}
                  </th>
                  {sql && (
                    <>
                      <th className="text-right">rows</th>
                      <th className="text-right">cache</th>
                      <th className="text-right">temp</th>
                    </>
                  )}
                </tr>
              </thead>
              <tbody>
                {list.map((q) => (
                  <tr
                    key={q.id}
                    aria-selected={q.id === selected}
                    className={`cursor-pointer ${q.id === selected ? "[&>td]:bg-(--g-accent-bg)!" : ""}`}
                    onClick={() => onSelect(q.id === selected ? null : q.id)}
                  >
                    <td className="max-w-[460px] truncate font-mono text-xs">
                      <button
                        type="button"
                        className="max-w-full truncate text-left"
                        onClick={(e) => {
                          e.stopPropagation();
                          onSelect(q.id === selected ? null : q.id);
                        }}
                      >
                        {q.text || "(text hidden)"}
                      </button>
                    </td>
                    {sql && (
                      <td className="text-xs text-(--g-dim)">{q.user}</td>
                    )}
                    <Num>{count(q.calls)}</Num>
                    <Num>{ms(q.total_ms)}</Num>
                    <Num>{ms(q.mean_ms)}</Num>
                    <Num>
                      {showChanges || (!sql && !q.max_ms) ? "" : ms(q.max_ms)}
                    </Num>
                    {sql && (
                      <>
                        <Num>{count(q.rows)}</Num>
                        <Num>{percent(q.cache_hit)}</Num>
                        <Num warn={q.temp_bytes > 0}>
                          {q.temp_bytes ? byteSize(q.temp_bytes) : "0"}
                        </Num>
                      </>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </Scroll>
          {list.length === 0 &&
            !(ins.unavailable ?? []).some((u) => u.view === "queries") && (
              <p className="px-3 py-3 text-(--g-mut)">
                {showChanges
                  ? `No ${noun.slice(0, -1)} ran between the two reads.`
                  : `No ${noun} yet.`}
              </p>
            )}
        </div>
      </div>
      {current && (
        <StatementPane
          st={current}
          share={totalAll ? current.total_ms / totalAll : undefined}
          tables={ins.tables ?? []}
          // MySQL's digests have ? for values: nothing to explain.
          onExplain={
            canRead && ins.engine === "postgres" ? onExplain : undefined
          }
          onClose={() => onSelect(null)}
        />
      )}
    </div>
  );
}

function Num({ children, warn }: { children: ReactNode; warn?: boolean }) {
  return (
    <td
      className={`text-right font-mono text-xs ${warn ? "text-(--g-warn)" : "text-(--g-dim)"}`}
    >
      {children}
    </td>
  );
}

/** One statement in full, its numbers and the tables it names. */
function StatementPane({
  st,
  share,
  tables,
  onExplain,
  onClose,
}: {
  st: DataStatement;
  share: number | undefined;
  tables: readonly DataTableStats[];
  onExplain?: (sql: string) => void;
  onClose: () => void;
}) {
  const named = tables.filter((t) =>
    new RegExp(`\\b${escapeRegExp(t.name)}\\b`, "i").test(st.text),
  );
  return (
    <aside className="g-aside w-[380px] shrink-0 gap-3!" aria-label="Statement">
      <div className="flex items-center gap-2">
        <h2 className="text-sm font-semibold">Statement</h2>
        <span className="truncate font-mono text-2xs text-(--g-mut)">
          {st.id}
        </span>
        <Btn small ghost className="ml-auto" onClick={onClose}>
          Close <Kbd>Esc</Kbd>
        </Btn>
      </div>
      <pre className="max-h-64 overflow-auto rounded-lg border border-(--g-line) bg-(--g-raise) p-2 text-xs whitespace-pre-wrap text-(--g-code)">
        {st.text || "(text hidden)"}
      </pre>
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-0.5 text-xs">
        <dt className="text-(--g-mut)">calls</dt>
        <dd className="text-right font-mono">{count(st.calls)}</dd>
        <dt className="text-(--g-mut)">total</dt>
        <dd className="text-right font-mono">
          {ms(st.total_ms)}
          {share !== undefined &&
            ` (${Math.round(share * 100)}% of those shown)`}
        </dd>
        <dt className="text-(--g-mut)">mean / max</dt>
        <dd className="text-right font-mono">
          {ms(st.mean_ms)} / {ms(st.max_ms)}
        </dd>
        <dt className="text-(--g-mut)">rows per call</dt>
        <dd className="text-right font-mono">
          {st.calls ? count(Math.round(st.rows / st.calls)) : "-"}
        </dd>
        <dt className="text-(--g-mut)">cache hit</dt>
        <dd className="text-right font-mono">{percent(st.cache_hit)}</dd>
      </dl>
      {named.length > 0 && (
        <section className="flex flex-col gap-1 text-xs">
          <h3 className="text-2xs text-(--g-mut)">tables it names</h3>
          {named.map((t) => (
            <p key={`${t.schema}.${t.name}`} className="font-mono">
              {t.schema}.{t.name}{" "}
              <span className="text-(--g-dim)">
                {count(t.seq_scans)} full scans, {count(t.index_scans)} index
                scans
              </span>
            </p>
          ))}
        </section>
      )}
      <div className="flex flex-wrap gap-1.5">
        {onExplain && st.text && (
          <Btn
            small
            primary
            onClick={() => onExplain(`EXPLAIN (GENERIC_PLAN)\n${st.text}`)}
          >
            Explain in SQL
          </Btn>
        )}
        {st.text && <CopyBtn text={st.text} />}
      </div>
      {onExplain && st.text && (
        <p className="text-2xs text-(--g-mut)">
          Explain opens EXPLAIN (GENERIC_PLAN) in the SQL tab (Postgres 16 and
          later), read only.
        </p>
      )}
    </aside>
  );
}

const escapeRegExp = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

/** Sessions, those blocking others first, with Follow. */
function Sessions({
  ins,
  selected,
  onSelect,
  onRefresh,
  access,
}: {
  ins: DataInsights;
  selected: string | null;
  onSelect: (id: string | null) => void;
  onRefresh: () => void;
  access: DataPermission;
}) {
  const [showIdle, setShowIdle] = useState(false);
  const following = useFollow(onRefresh);
  const all = ins.sessions ?? [];
  const blocks = new Map<string, number>();
  for (const s of all)
    for (const b of s.blocked_by ?? []) blocks.set(b, (blocks.get(b) ?? 0) + 1);
  const idle = all.filter((s) => s.state === "idle").length;
  const shown = ordered(
    all.filter((s) => showIdle || s.state !== "idle"),
    blocks,
  );
  const current = all.find((s) => s.id === selected);
  // Redis' clients have IDs and run commands, without transactions.
  const redis = ins.engine === "redis";
  const id = redis ? "id" : "pid";
  return (
    <div className="flex min-h-0 flex-1">
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 border-b border-(--g-line) px-3 py-1.5 text-xs text-(--g-mut)">
          <Btn
            small
            aria-pressed={showIdle}
            onClick={() => setShowIdle((v) => !v)}
          >
            {showIdle ? "Hide idle" : `Show idle (${idle})`}
          </Btn>
          <Btn
            small
            aria-pressed={following.on}
            onClick={following.toggle}
            title="Refresh every 5 s while this tab is visible, for up to 10 minutes"
          >
            {following.on ? "Stop following" : "Follow"}
          </Btn>
          {following.on && <span>every 5 s</span>}
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto">
          <Scroll>
            <table className="g-table">
              <thead>
                <tr>
                  <th>{id}</th>
                  <th>application</th>
                  <th>user</th>
                  <th>state</th>
                  <th className="text-right">for</th>
                  <th>waiting on</th>
                  <th>{redis ? "command" : "statement"}</th>
                </tr>
              </thead>
              <tbody>
                {shown.map(({ s, depth }) => (
                  <tr
                    key={s.id}
                    aria-selected={s.id === selected}
                    className={`cursor-pointer ${s.id === selected ? "[&>td]:bg-(--g-accent-bg)!" : ""}`}
                    onClick={() => onSelect(s.id === selected ? null : s.id)}
                  >
                    <td className="font-mono text-xs">
                      {depth > 0 && (
                        <span className="text-(--g-faint)">
                          {" ".repeat((depth - 1) * 2)}└{" "}
                        </span>
                      )}
                      <button
                        type="button"
                        onClick={(e) => {
                          e.stopPropagation();
                          onSelect(s.id === selected ? null : s.id);
                        }}
                      >
                        {s.pid ?? s.id}
                      </button>
                    </td>
                    <td>{sessionApp(s)}</td>
                    <td className="text-xs text-(--g-dim)">{s.user}</td>
                    <td className={stateTone(s, blocks)}>
                      {stateWords(s, blocks)}
                    </td>
                    <Num>{forAt(ins, sessionSince(s))}</Num>
                    <td className="font-mono text-xs text-(--g-dim)">
                      {(s.blocked_by ?? []).map(subjectLabel).join(", ")}
                    </td>
                    <td className="max-w-[420px] truncate font-mono text-xs">
                      {s.text}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Scroll>
          {shown.length === 0 && (
            <p className="px-3 py-3 text-(--g-mut)">
              Nothing is running. {idle} idle{" "}
              {redis
                ? idle === 1
                  ? "client"
                  : "clients"
                : idle === 1
                  ? "session"
                  : "sessions"}
              .
            </p>
          )}
        </div>
      </div>
      {current && (
        <aside
          className="g-aside w-[380px] shrink-0 gap-3!"
          aria-label="Session"
        >
          <div className="flex items-center gap-2">
            <h2 className="text-sm font-semibold">
              {sessionApp(current)} · {id} {current.pid}
            </h2>
            <Btn small ghost className="ml-auto" onClick={() => onSelect(null)}>
              Close <Kbd>Esc</Kbd>
            </Btn>
          </div>
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-0.5 text-xs">
            <dt className="text-(--g-mut)">user</dt>
            <dd>{current.user}</dd>
            <dt className="text-(--g-mut)">client</dt>
            <dd className="font-mono">{current.client || "-"}</dd>
            <dt className="text-(--g-mut)">state</dt>
            <dd>{stateWords(current, blocks)}</dd>
            {!redis && (
              <>
                <dt className="text-(--g-mut)">transaction</dt>
                <dd className="font-mono">
                  {forAt(ins, current.transaction_at)}
                </dd>
              </>
            )}
            <dt className="text-(--g-mut)">
              {redis ? "command" : "statement"}
            </dt>
            <dd className="font-mono">{forAt(ins, current.query_at)}</dd>
            {current.wait && (
              <>
                <dt className="text-(--g-mut)">waits for</dt>
                <dd className="font-mono">{current.wait}</dd>
              </>
            )}
          </dl>
          {current.text && (
            <pre className="max-h-64 overflow-auto rounded-lg border border-(--g-line) bg-(--g-raise) p-2 text-xs whitespace-pre-wrap text-(--g-code)">
              {current.text}
            </pre>
          )}
          {current.kind === "client" && (
            <SessionActions
              key={current.id}
              session={current}
              redis={redis}
              access={access}
              onDone={() => {
                onSelect(null);
                onRefresh();
              }}
            />
          )}
        </aside>
      )}
    </div>
  );
}

/** Follow: refresh every followEvery while the page is visible, at most followFor. */
function useFollow(refresh: () => void) {
  const [on, setOn] = useState(false);
  const tick = useEffectEvent(refresh);
  useEffect(() => {
    if (!on) return;
    const started = Date.now();
    const timer = setInterval(() => {
      if (Date.now() - started > followFor) setOn(false);
      else if (document.visibilityState === "visible") tick();
    }, followEvery);
    return () => clearInterval(timer);
  }, [on]);
  return { on, toggle: () => setOn((v) => !v) };
}

/** Sessions with each blocked one under its blocker, roots first. */
function ordered(list: DataSession[], blocks: Map<string, number>) {
  const ids = new Set(list.map((s) => s.id));
  const children = new Map<string, DataSession[]>();
  const roots: DataSession[] = [];
  for (const s of list) {
    const parent = (s.blocked_by ?? []).find((b) => ids.has(b));
    if (parent) children.set(parent, [...(children.get(parent) ?? []), s]);
    else roots.push(s);
  }
  roots.sort((a, b) => (blocks.get(b.id) ?? 0) - (blocks.get(a.id) ?? 0));
  const out: { s: DataSession; depth: number }[] = [];
  const seen = new Set<string>();
  const walk = (s: DataSession, depth: number) => {
    if (seen.has(s.id)) return;
    seen.add(s.id);
    out.push({ s, depth });
    for (const c of children.get(s.id) ?? []) walk(c, depth + 1);
  };
  for (const r of roots) walk(r, 0);
  return out;
}

const sessionApp = (s: DataSession) =>
  s.kind === "dock"
    ? "Dock"
    : s.kind === "autovacuum"
      ? "autovacuum"
      : s.application || "(no name)";

const sessionSince = (s: DataSession) =>
  s.state === "active" ? s.query_at : s.state_at;

function stateWords(s: DataSession, blocks: Map<string, number>) {
  if (s.blocked_by?.length) return "waiting on a lock";
  const n = blocks.get(s.id);
  const base = s.state.replaceAll("_", " ");
  return n ? `${base}, blocks ${n}` : base;
}

function stateTone(s: DataSession, blocks: Map<string, number>) {
  return s.blocked_by?.length ||
    blocks.has(s.id) ||
    s.state === "idle_in_transaction"
    ? "text-xs text-(--g-warn)"
    : "text-xs text-(--g-dim)";
}

/** Hints by subject, for the notes columns. */
const hintsOf = (ins: DataInsights, view: DataInsightView) => {
  const m = new Map<string, DataHint[]>();
  for (const h of ins.hints ?? [])
    if (h.view === view && h.subject)
      m.set(h.subject, [...(m.get(h.subject) ?? []), h]);
  return m;
};

function Tables({
  ins,
  onOpen,
}: {
  ins: DataInsights;
  onOpen: (view: DataInsightView, subject?: string) => void;
}) {
  const notes = hintsOf(ins, "tables");
  const indexNotes = hintsOf(ins, "indexes");
  // MySQL counts rows read, not scans, and has no vacuum or dead rows.
  const pg = ins.engine === "postgres";
  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <Scroll>
        <table className="g-table">
          <thead>
            <tr>
              <th>table</th>
              <th className="text-right">size</th>
              <th className="text-right">indexes</th>
              <th className="text-right">rows</th>
              {pg ? (
                <>
                  <th className="text-right">dead</th>
                  <th className="text-right">full scans</th>
                  <th className="text-right">index scans</th>
                  <th className="text-right">vacuumed</th>
                  <th className="text-right">cache</th>
                </>
              ) : (
                <>
                  <th className="text-right">rows by full scans</th>
                  <th className="text-right">rows by index</th>
                </>
              )}
              <th>notes</th>
            </tr>
          </thead>
          <tbody>
            {(ins.tables ?? []).map((t) => {
              const name = `${t.schema}.${t.name}`;
              const hs = notes.get(name) ?? [];
              const idx = [...indexNotes.keys()].filter((k) =>
                (ins.indexes ?? []).some(
                  (x) =>
                    `${x.schema}.${x.name}` === k &&
                    x.table === t.name &&
                    x.schema === t.schema,
                ),
              );
              const dead = t.estimated_rows
                ? t.dead_rows / (t.estimated_rows + t.dead_rows)
                : 0;
              return (
                <tr key={name}>
                  <td className="font-mono text-xs">{name}</td>
                  <Num>
                    {t.total_bytes !== undefined
                      ? byteSize(t.total_bytes)
                      : `~${byteSize(t.estimated_bytes)} (locked)`}
                  </Num>
                  <Num>
                    {t.index_bytes !== undefined
                      ? byteSize(t.index_bytes)
                      : "-"}
                  </Num>
                  <Num>{count(t.estimated_rows)}</Num>
                  {pg ? (
                    <>
                      <Num warn={hs.some((h) => h.kind === "dead_rows")}>
                        {`${Math.round(dead * 100)}%`}
                      </Num>
                      <Num warn={hs.some((h) => h.kind === "full_scans")}>
                        {count(t.seq_scans)}
                      </Num>
                      <Num>{count(t.index_scans)}</Num>
                      <Num>{forAt(ins, t.vacuumed_at)}</Num>
                      <Num>{percent(t.cache_hit)}</Num>
                    </>
                  ) : (
                    <>
                      <Num warn={hs.some((h) => h.kind === "full_scans")}>
                        {count(t.seq_rows_read)}
                      </Num>
                      <Num>{count(t.index_scans)}</Num>
                    </>
                  )}
                  <td className="text-xs text-(--g-dim)">
                    <span className="inline-flex items-center gap-2">
                      {hs.map((h) => (
                        <span
                          key={h.kind}
                          className={
                            h.level === "warn" ? "text-(--g-warn)" : ""
                          }
                        >
                          {h.title.toLowerCase()}
                        </span>
                      ))}
                      {idx.length > 0 && (
                        <button
                          type="button"
                          className="g-lnk"
                          onClick={() => onOpen("indexes")}
                        >
                          {idx.length} {idx.length === 1 ? "index" : "indexes"}{" "}
                          to look at
                        </button>
                      )}
                      {hs.find((h) => h.copy) && (
                        <CopyBtn
                          text={hs.find((h) => h.copy)?.copy ?? ""}
                          label="Copy statement"
                        />
                      )}
                    </span>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </Scroll>
    </div>
  );
}

/**
 * Redis' key prefixes by estimated size, from a sample of each
 * database's keys, with the sample's biggest key of each.
 */
function KeyPrefixes({ ins }: { ins: DataInsights }) {
  const notes = hintsOf(ins, "keys");
  const list: DataKeyPrefix[] = ins.keys ?? [];
  const total = list.reduce((n, p) => n + p.estimated_bytes, 0);
  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <Scroll>
        <table className="g-table">
          <thead>
            <tr>
              <th>prefix</th>
              <th className="text-right">db</th>
              <th className="text-right">keys</th>
              <th className="text-right">size</th>
              <th className="text-right">share</th>
              <th>biggest key</th>
              <th className="text-right">its size</th>
              <th>notes</th>
            </tr>
          </thead>
          <tbody>
            {list.map((p) => (
              <tr key={`${p.db}:${p.prefix}`}>
                <td className="font-mono text-xs">
                  {p.prefix || (
                    <span className="text-(--g-dim)">no prefix</span>
                  )}
                </td>
                <Num>{p.db}</Num>
                <Num>~{count(p.estimated_keys)}</Num>
                <Num>~{byteSize(p.estimated_bytes)}</Num>
                <Num>
                  {percent(total ? p.estimated_bytes / total : undefined)}
                </Num>
                <td className="max-w-[320px] truncate font-mono text-xs">
                  {p.biggest}
                </td>
                <Num warn={notes.has(p.biggest)}>
                  {byteSize(p.biggest_bytes)}
                </Num>
                <td className="text-xs text-(--g-warn)">
                  {notes
                    .get(p.biggest)
                    ?.map((h) => h.title)
                    .join(", ")}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Scroll>
      <p className="px-3 py-2.5 text-xs text-(--g-mut)">
        {list.length
          ? `Estimated from a sample of ${count(list.reduce((n, p) => n + p.sampled_keys, 0))} keys.`
          : "No keys."}
      </p>
    </div>
  );
}

function Indexes({ ins }: { ins: DataInsights }) {
  const notes = hintsOf(ins, "indexes");
  const list = [...(ins.indexes ?? [])].sort(
    (a, b) =>
      +notes.has(`${b.schema}.${b.name}`) - +notes.has(`${a.schema}.${a.name}`),
  );
  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <Scroll>
        <table className="g-table">
          <thead>
            <tr>
              <th>index</th>
              <th>table</th>
              <th className="text-right">size</th>
              <th className="text-right">scans</th>
              <th className="text-right">last scan</th>
              <th>kind</th>
              <th>notes</th>
            </tr>
          </thead>
          <tbody>
            {list.map((x) => {
              const hs = notes.get(`${x.schema}.${x.name}`) ?? [];
              return (
                <tr key={`${x.schema}.${x.name}`} title={x.definition}>
                  <td className="font-mono text-xs">{x.name}</td>
                  <td className="font-mono text-xs text-(--g-dim)">
                    {x.schema}.{x.table}
                  </td>
                  <Num>
                    {x.bytes !== undefined ? byteSize(x.bytes) : "(locked)"}
                  </Num>
                  <Num warn={x.scans === 0 && !x.constraint}>
                    {count(x.scans)}
                  </Num>
                  <Num>
                    {x.last_scan_at ? `${forAt(ins, x.last_scan_at)} ago` : "-"}
                  </Num>
                  <td className="text-xs text-(--g-dim)">{indexKind(x)}</td>
                  <td className="text-xs">
                    <span className="inline-flex items-center gap-2">
                      {hs.map((h) => (
                        <span
                          key={h.kind}
                          className={
                            h.level === "warn"
                              ? "text-(--g-warn)"
                              : "text-(--g-dim)"
                          }
                        >
                          {h.title.toLowerCase()}: {h.detail}
                        </span>
                      ))}
                      {hs[0]?.copy && (
                        <CopyBtn text={hs[0].copy} label="Copy statement" />
                      )}
                    </span>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </Scroll>
    </div>
  );
}

const indexKind = (x: DataIndexStats) =>
  x.primary
    ? "primary key"
    : x.constraint
      ? "constraint"
      : x.unique
        ? "unique"
        : !x.valid
          ? "invalid"
          : "";

/**
 * Turning on query stats when they are off: what it involves in the
 * host's words, and the action where the caller may take it (Dock stages
 * the start command change; creating the extension is for admins).
 */
function QueryStatsOff({
  engine,
  state,
  access,
  onDone,
}: {
  engine: DataInsights["engine"];
  state: DataOverview["query_stats"];
  access: DataPermission;
  onDone: () => void;
}) {
  const { words } = useDataSource();
  const cli = useCli();
  const enable = useEnableQueryStats(onDone);
  const notLoaded = state === "not_loaded";
  if (engine === "mysql") {
    return (
      <div className="border-b border-(--g-line) px-3 py-2.5 text-xs text-(--g-dim)">
        performance_schema is off on this server, so statements aren't counted.
        Start it with --performance-schema=ON and deploy (a restart).
      </div>
    );
  }
  const canAct = notLoaded ? words.stagesQueryStats : access.manage;
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-(--g-line) px-3 py-2.5 text-xs text-(--g-dim)">
      <span className="max-w-[640px]">
        {notLoaded
          ? words.queryStatsOff
          : "The server loads pg_stat_statements, but its extension isn't created in this database."}
        {!canAct && !notLoaded && " An admin can create it here."}
      </span>
      {enable.data ? (
        <span className="text-(--g-ok)">{enable.data.detail}</span>
      ) : (
        canAct && (
          <Act
            small
            primary
            cli={cli("dock db queries enable")}
            disabled={enable.isPending}
            onClick={() => enable.mutate()}
          >
            {notLoaded ? "Turn on query stats" : "Create the extension"}
          </Act>
        )
      )}
      <ErrorText error={enable.error} />
    </div>
  );
}

/** POST /data/insights/queries/enable, then read again. */
function useEnableQueryStats(onDone: () => void) {
  const source = useDataSource();
  return useMutation({
    mutationFn: () => source.enableQueryStats(),
    onSuccess: onDone,
  });
}

/** Reset query stats (admins), asking first in place. */
function ResetStats({ onDone }: { onDone: () => void }) {
  const source = useDataSource();
  const cli = useCli();
  const [asking, setAsking] = useState(false);
  const reset = useMutation({
    mutationFn: () => source.resetQueryStats(),
    onSuccess: () => {
      setAsking(false);
      onDone();
    },
  });
  if (!asking)
    return (
      <Btn small onClick={() => setAsking(true)}>
        Reset
      </Btn>
    );
  return (
    <span className="flex items-center gap-2 text-(--g-text)">
      Every statement counts from zero again.
      <Btn small onClick={() => setAsking(false)}>
        Cancel
      </Btn>
      <Act
        small
        danger
        cli={cli("dock db queries reset")}
        disabled={reset.isPending}
        onClick={() => reset.mutate()}
      >
        Reset query stats
      </Act>
      <ErrorText error={reset.error} />
    </span>
  );
}

/**
 * Cancel a session's query, or end the session, from its side pane.
 * Needs level write. Cancelling asks nothing (the client just gets an
 * error); ending the session asks first, and on production for the
 * name. Redis can't cancel a command: its clients can only be ended.
 */
function SessionActions({
  session,
  redis,
  access,
  onDone,
}: {
  session: DataSession;
  redis: boolean;
  access: DataPermission;
  onDone: () => void;
}) {
  const source = useDataSource();
  const cli = useCli();
  const [ending, setEnding] = useState(false);
  const [confirm, setConfirm] = useState("");
  const act = useMutation({
    mutationFn: (terminate: boolean) =>
      source.cancelSession(session.id, {
        terminate,
        query_started_at: terminate ? undefined : session.query_at,
        confirm: terminate && access.confirm_commit ? confirm : undefined,
      }),
    onSuccess: onDone,
  });
  if (access.level !== "write")
    return (
      <p className="text-2xs text-(--g-mut)">
        Cancelling queries and ending sessions needs data access level write.
      </p>
    );
  const confirmed = !access.confirm_commit || confirm === source.confirmName;
  const end = redis ? "End client" : "End session";
  return (
    <section className="flex flex-col gap-2 border-t border-(--g-line) pt-3">
      {!ending ? (
        <div className="flex flex-wrap gap-1.5">
          {session.state === "active" && !redis && (
            <Act
              small
              cli={cli(`dock db cancel ${session.id}`)}
              disabled={act.isPending}
              onClick={() => act.mutate(false)}
            >
              Cancel query
            </Act>
          )}
          <Btn small danger onClick={() => setEnding(true)}>
            {end}
          </Btn>
        </div>
      ) : (
        <>
          <p className="text-xs text-(--g-dim)">
            {redis
              ? "Its connection drops and commands queued in a MULTI are discarded."
              : "Its connection drops and its open transaction rolls back."}{" "}
            The app sees an error and has to reconnect.
          </p>
          {access.confirm_commit && (
            <ConfirmName verb="end it" value={confirm} onChange={setConfirm} />
          )}
          <div className="flex flex-wrap gap-1.5">
            <Btn small onClick={() => setEnding(false)}>
              Keep it
            </Btn>
            <Act
              small
              danger
              cli={cli(
                `dock db cancel ${session.id} --terminate${access.confirm_commit ? ` --confirm ${source.confirmName}` : ""}`,
              )}
              disabled={act.isPending || !confirmed}
              onClick={() => act.mutate(true)}
            >
              {end}
            </Act>
          </div>
        </>
      )}
      <ErrorText error={act.error} />
    </section>
  );
}
