import {
  type ReactNode,
  useEffect,
  useEffectEvent,
  useRef,
  useState,
} from "react";
import { useShortcuts } from "../../shell/keys";
import { clockSec, elapsed } from "../../ui/format";
import { useCli, useInsights } from "./api";
import { byteSize } from "./cells";
import { Act, Btn, CopyBtn, ErrorText, Kbd, Scroll } from "./kit";
import type {
  DataHint,
  DataIndexStats,
  DataInsights,
  DataInsightView,
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
// side. Nothing refreshes by itself except Follow on Sessions, a timer
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
};
const views = Object.keys(viewLabels) as DataInsightView[];

const orders: { order: DataStatementOrder; label: string }[] = [
  { order: "total", label: "total" },
  { order: "mean", label: "mean" },
  { order: "calls", label: "calls" },
  { order: "rows", label: "rows" },
];

export default function HealthView({
  access,
  view,
  onView,
  onExplain,
}: {
  access: DataPermission;
  view: DataInsightView;
  onView: (view: DataInsightView) => void;
  /** Open SQL in the SQL tab. */
  onExplain: (sql: string) => void;
}) {
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
            {viewLabels[v]}
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
            .filter((u) => u.view === view)
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
            />
          ) : view === "sessions" ? (
            <Sessions
              ins={ins}
              selected={selected}
              onSelect={setSelected}
              onRefresh={refresh}
            />
          ) : view === "tables" ? (
            <Tables ins={ins} onOpen={open} />
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
        cache
      </span>
      <span>
        <span className="font-mono text-(--g-text)">{byteSize(ov.bytes)}</span>
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
      {ov && (
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
          <Fact k="spilled to disk">{byteSize(ov.temp_bytes)}</Fact>
          <Fact k="deadlocks">{ov.deadlocks}</Fact>
          {ov.transaction_id_age !== undefined && (
            <Fact k="transaction ID age">
              {`${Math.round(ov.transaction_id_age / 1e6)}M`}
            </Fact>
          )}
        </dl>
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
}: {
  ins: DataInsights;
  previous: DataInsights | undefined;
  order: DataStatementOrder;
  onOrder: (o: DataStatementOrder) => void;
  selected: string | null;
  onSelect: (id: string | null) => void;
  onExplain: (sql: string) => void;
  canRead: boolean;
}) {
  const [changes, setChanges] = useState(false);
  const showChanges = changes && !!previous;
  const list =
    showChanges && previous ? deltas(ins, previous) : (ins.queries ?? []);
  const current = list.find((q) => q.id === selected);
  const all = ins.queries ?? [];
  const totalAll = all.reduce((n, q) => n + q.total_ms, 0);
  return (
    <div className="flex min-h-0 flex-1">
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 border-b border-(--g-line) px-3 py-1.5 text-xs text-(--g-mut)">
          <span className="flex items-center gap-2">
            order
            <fieldset className="g-seg" aria-label="Order">
              {orders.map((o) => (
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
                since {ins.since_reason === "reset" ? "reset" : "start"}
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
              {all.length} of {count(ins.queries_tracked)} statements · Dock's
              own hidden
            </span>
          )}
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto">
          <Scroll>
            <table className="g-table">
              <thead>
                <tr>
                  <th>statement</th>
                  <th>user</th>
                  <th className="text-right">calls</th>
                  <th className="text-right">total</th>
                  <th className="text-right">mean</th>
                  <th className="text-right">max</th>
                  <th className="text-right">rows</th>
                  <th className="text-right">cache</th>
                  <th className="text-right">temp</th>
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
                    <td className="text-xs text-(--g-dim)">{q.user}</td>
                    <Num>{count(q.calls)}</Num>
                    <Num>{ms(q.total_ms)}</Num>
                    <Num>{ms(q.mean_ms)}</Num>
                    <Num>{showChanges ? "" : ms(q.max_ms)}</Num>
                    <Num>{count(q.rows)}</Num>
                    <Num>{percent(q.cache_hit)}</Num>
                    <Num warn={q.temp_bytes > 0}>
                      {q.temp_bytes ? byteSize(q.temp_bytes) : "0"}
                    </Num>
                  </tr>
                ))}
              </tbody>
            </table>
          </Scroll>
          {list.length === 0 &&
            !(ins.unavailable ?? []).some((u) => u.view === "queries") && (
              <p className="px-3 py-3 text-(--g-mut)">
                {showChanges
                  ? "No statement ran between the two reads."
                  : "No statements yet."}
              </p>
            )}
        </div>
      </div>
      {current && (
        <StatementPane
          st={current}
          share={totalAll ? current.total_ms / totalAll : undefined}
          tables={ins.tables ?? []}
          onExplain={canRead ? onExplain : undefined}
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
}: {
  ins: DataInsights;
  selected: string | null;
  onSelect: (id: string | null) => void;
  onRefresh: () => void;
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
                  <th>pid</th>
                  <th>application</th>
                  <th>user</th>
                  <th>state</th>
                  <th className="text-right">for</th>
                  <th>waiting on</th>
                  <th>statement</th>
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
              {idle === 1 ? "session" : "sessions"}.
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
              {sessionApp(current)} · pid {current.pid}
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
            <dt className="text-(--g-mut)">transaction</dt>
            <dd className="font-mono">{forAt(ins, current.transaction_at)}</dd>
            <dt className="text-(--g-mut)">statement</dt>
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
              <th className="text-right">dead</th>
              <th className="text-right">full scans</th>
              <th className="text-right">index scans</th>
              <th className="text-right">vacuumed</th>
              <th className="text-right">cache</th>
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
                  <Num warn={hs.some((h) => h.kind === "dead_rows")}>
                    {`${Math.round(dead * 100)}%`}
                  </Num>
                  <Num warn={hs.some((h) => h.kind === "full_scans")}>
                    {count(t.seq_scans)}
                  </Num>
                  <Num>{count(t.index_scans)}</Num>
                  <Num>{forAt(ins, t.vacuumed_at)}</Num>
                  <Num>{percent(t.cache_hit)}</Num>
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
