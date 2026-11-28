import { Link } from "@tanstack/react-router";
import { lazy, Suspense, useState } from "react";
import { useServers, useSession } from "../../data/queries";
import { ago, plural } from "../../ui/format";
import {
  Act,
  ErrorText,
  Fact,
  Facts,
  Page,
  PageBar,
  Panel,
  SectionHead,
} from "../../ui/kit";
import {
  type HostCheck,
  type HostChecks,
  rank,
  useCheckHost,
  useHostChecks,
  useRestoreFinding,
} from "./data";

const DismissFinding = lazy(() => import("./DismissFinding"));

/** A result with the server it is from. */
type Row = { server: string; check: HostCheck };

/**
 * The console's Security page (ADR 0109, mock B): every server's open
 * host check findings in one list, worst first, then what couldn't be
 * told, what was dismissed and why, and each server with its last check
 * and Check now. Operators act; platform viewers read.
 */
export function SecurityPage() {
  const all = useHostChecks();
  const servers = useServers();
  const operator = useSession()?.platform === "operator";
  const check = useCheckHost();
  const restore = useRestoreFinding();
  const [checking, setChecking] = useState<ReadonlySet<string>>(new Set());
  // Why each server's last Check now failed, shown beside it until retried.
  // A Map, so a server named like an Object member reads nothing.
  const [failed, setFailed] = useState<ReadonlyMap<string, string>>(new Map());
  const [dismissing, setDismissing] = useState<Row | null>(null);

  const rows = all.flatMap((s) =>
    (s.checks ?? []).map((check): Row => ({ server: s.server, check })),
  );
  const open = rows
    .filter((r) => r.check.status === "finding" && !r.check.dismissed)
    .sort(
      (a, b) =>
        rank(b.check) - rank(a.check) ||
        (a.check.first_seen_at ?? "").localeCompare(
          b.check.first_seen_at ?? "",
        ),
    );
  const unknown = rows.filter((r) => r.check.status === "unknown");
  const dismissed = rows.filter((r) => r.check.dismissed);
  const total = (k: "critical" | "warning" | "info") =>
    all.reduce((n, s) => n + s.counts[k], 0);
  const online = new Set(
    servers.filter((s) => s.status === "online").map((s) => s.name),
  );

  const run = async (names: string[]) => {
    setChecking((was) => new Set([...was, ...names]));
    setFailed((was) => new Map([...was].filter(([n]) => !names.includes(n))));
    await Promise.allSettled(
      names.map((n) =>
        check
          .mutateAsync(n)
          .catch((e: unknown) =>
            setFailed((was) =>
              new Map(was).set(n, e instanceof Error ? e.message : String(e)),
            ),
          )
          .finally(() =>
            setChecking((was) => new Set([...was].filter((x) => x !== n))),
          ),
      ),
    );
  };
  const checkable = all.map((s) => s.server).filter((n) => online.has(n));
  const last = all
    .map((s) => s.checked_at)
    .filter((t): t is string => !!t)
    .sort()
    .at(-1);

  return (
    <>
      <PageBar
        title="Security"
        after={
          last && <span className="text-xs text-mut">checked {ago(last)}</span>
        }
        actions={
          operator && (
            <Act
              cli="dock server checks <name> --now"
              disabled={checking.size > 0 || checkable.length === 0}
              onClick={() => run(checkable)}
            >
              {checking.size > 0 ? "Checking" : "Check all now"}
            </Act>
          )
        }
      />
      <Page>
        <Facts>
          <Fact label="critical">
            <span className={total("critical") ? "text-fail" : ""}>
              {total("critical")}
            </span>
          </Fact>
          <Fact label="warnings">
            <span className={total("warning") ? "text-warn-text" : ""}>
              {total("warning")}
            </span>
          </Fact>
          <Fact label="info">{total("info")}</Fact>
          <Fact label="dismissed">{dismissed.length}</Fact>
        </Facts>
        <ErrorText error={restore.error} />

        <section aria-label="Findings">
          {open.length === 0 && unknown.length === 0 ? (
            <p className="text-mut">{allClear(all)}</p>
          ) : (
            <Panel>
              <table>
                <thead>
                  <tr>
                    <th className="w-6" aria-label="Severity" />
                    <th>Server</th>
                    <th>Finding</th>
                    <th>What to do</th>
                    <th>Found</th>
                    {operator && <th aria-label="Actions" />}
                  </tr>
                </thead>
                <tbody>
                  {[...open, ...unknown].slice(0, maxRows).map((r) => (
                    <tr key={`${r.server}/${r.check.key}`}>
                      <td>
                        <Dot check={r.check} />
                      </td>
                      <td className="font-mono">
                        <Link
                          to="/console/servers/$server"
                          params={{ server: r.server }}
                          className="hover:text-fg"
                        >
                          {r.server}
                        </Link>
                      </td>
                      <td className="min-w-[240px] whitespace-normal">
                        <div>{r.check.title}</div>
                        {r.check.detail && (
                          <div className="text-xs text-mut">
                            {r.check.detail}
                          </div>
                        )}
                      </td>
                      <td className="min-w-[200px] text-xs whitespace-normal text-sec">
                        <Commands text={r.check.fix ?? ""} />
                      </td>
                      <td className="whitespace-nowrap text-mut">
                        {r.check.status === "unknown"
                          ? "can't tell"
                          : r.check.first_seen_at && ago(r.check.first_seen_at)}
                      </td>
                      {operator && (
                        <td className="text-right">
                          {r.check.status === "finding" && (
                            <Act
                              small
                              cli={`dock server checks dismiss ${r.server} ${r.check.key} --reason "..."`}
                              onClick={() => setDismissing(r)}
                            >
                              Dismiss…
                            </Act>
                          )}
                        </td>
                      )}
                    </tr>
                  ))}
                </tbody>
              </table>
            </Panel>
          )}
          <More shown={maxRows} total={open.length + unknown.length} />
        </section>

        {dismissed.length > 0 && (
          <section aria-labelledby="dismissed">
            <SectionHead id="dismissed">Dismissed</SectionHead>
            <Panel>
              <table>
                <tbody>
                  {dismissed.slice(0, maxRows).map((r) => (
                    <tr key={`${r.server}/${r.check.key}`}>
                      <td className="font-mono">
                        <Link
                          to="/console/servers/$server"
                          params={{ server: r.server }}
                          className="hover:text-fg"
                        >
                          {r.server}
                        </Link>
                      </td>
                      <td>{r.check.title}</td>
                      <td className="min-w-[200px] whitespace-normal wrap-anywhere text-mut">
                        "{r.check.dismissed?.reason}" · {r.check.dismissed?.by},{" "}
                        {r.check.dismissed && ago(r.check.dismissed.at)}
                      </td>
                      {operator && (
                        <td className="text-right">
                          <Act
                            small
                            cli={`dock server checks restore ${r.server} ${r.check.key}`}
                            disabled={restore.isPending}
                            onClick={() =>
                              restore.mutate({
                                server: r.server,
                                finding: r.check.key,
                              })
                            }
                          >
                            Restore
                          </Act>
                        </td>
                      )}
                    </tr>
                  ))}
                </tbody>
              </table>
            </Panel>
            <More shown={maxRows} total={dismissed.length} />
          </section>
        )}

        <section aria-labelledby="servers">
          <SectionHead id="servers">Servers</SectionHead>
          <Panel>
            <table>
              <tbody>
                {all.map((s) => (
                  <tr key={s.server}>
                    <td className="w-40 font-mono">{s.server}</td>
                    <td>
                      <Summary checks={s} />
                      {failed.has(s.server) && (
                        <span role="alert" className="text-fail">
                          {" "}
                          · Check now failed: {failed.get(s.server)}
                        </span>
                      )}
                    </td>
                    <td className="text-mut">
                      {s.checked_at ? `checked ${ago(s.checked_at)}` : ""}
                    </td>
                    {operator && (
                      <td className="text-right">
                        <Act
                          small
                          cli={`dock server checks ${s.server} --now`}
                          disabled={
                            checking.has(s.server) || !online.has(s.server)
                          }
                          reason={online.has(s.server) ? undefined : "offline"}
                          onClick={() => run([s.server])}
                        >
                          {checking.has(s.server) ? "Checking" : "Check now"}
                        </Act>
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </Panel>
        </section>
      </Page>
      {dismissing && (
        <Suspense fallback={null}>
          <DismissFinding
            server={dismissing.server}
            check={dismissing.check}
            onClose={() => setDismissing(null)}
          />
        </Suspense>
      )}
    </>
  );
}

/** Rows a list shows at most; `dock server checks` lists every one. */
const maxRows = 200;

/** Says how many rows a capped list left out. */
function More({ shown, total }: { shown: number; total: number }) {
  if (total <= shown) return null;
  return (
    <p className="mt-2 text-xs text-mut">
      {total - shown} more not shown; <code>dock server checks NAME</code> lists
      a server's.
    </p>
  );
}

/** The line in place of findings when there are none: all clear only when every server checked. */
function allClear(all: HostChecks[]) {
  const unchecked = all.filter((s) => !s.checked_at).length;
  if (unchecked === all.length)
    return "No server has checked its host yet. Servers check about once a day.";
  // A last check that failed shows an earlier one (if any), and an unchecked
  // server shows nothing: neither is an all-clear.
  const failed = all.filter((s) => s.error).length;
  const notes = [
    unchecked > 0 &&
      `${unchecked === 1 ? "1 server hasn't" : `${unchecked} servers haven't`} checked yet`,
    failed > 0 &&
      `the last check failed on ${failed === 1 ? "1 server" : `${failed} servers`} (results shown, if any, are from an earlier check)`,
  ].filter(Boolean);
  if (notes.length > 0)
    return `Nothing to fix in the results shown; ${notes.join(", and ")}. See Servers below.`;
  return "Nothing to fix on any server.";
}

/** A finding's severity as a dot; grey for one that can't tell. */
function Dot({ check }: { check: HostCheck }) {
  const color =
    check.status !== "finding"
      ? "bg-dash"
      : check.severity === "critical"
        ? "bg-fail"
        : check.severity === "warning"
          ? "bg-warn"
          : "bg-info";
  return (
    <span
      role="img"
      aria-label={check.status === "finding" ? check.severity : "can't tell"}
      className={`inline-block size-2 rounded-full ${color}`}
    />
  );
}

/** A server's last check in a few words: "1 critical, 3 warnings" or why it failed. */
function Summary({ checks: s }: { checks: HostChecks }) {
  if (!s.checked_at) return <span className="text-mut">not checked yet</span>;
  const { critical, warning, info } = s.counts;
  const parts = [
    {
      key: "critical",
      n: critical,
      text: `${critical} critical`,
      tone: "text-fail",
    },
    {
      key: "warning",
      n: warning,
      text: plural(warning, "warning"),
      tone: "text-warn-text",
    },
    { key: "info", n: info, text: `${info} info`, tone: "" },
  ].filter((p) => p.n > 0);
  const passed = (s.checks ?? []).filter((c) => c.status === "pass").length;
  return (
    <span>
      {parts.length === 0 ? (
        <span className="text-ok">{plural(passed, "check")} pass</span>
      ) : (
        parts.map((p, i) => (
          <span key={p.key} className={p.tone}>
            {i > 0 && ", "}
            {p.text}
          </span>
        ))
      )}
      {s.error && (
        <span className="text-fail"> · last check failed: {s.error}</span>
      )}
    </span>
  );
}

/** Text with `commands` in backticks shown as code. */
function Commands({ text }: { text: string }) {
  if (!text) return <span className="text-mut">-</span>;
  return (
    <>
      {codeParts(text).map(({ part, code, at }) =>
        code ? (
          <code key={at} className="font-mono text-fg">
            {part}
          </code>
        ) : (
          part
        ),
      )}
    </>
  );
}

/** Splits text on backticks: each part, whether it is code, and where it starts (a key). */
function codeParts(text: string) {
  let at = 0;
  return text.split("`").map((part, i) => {
    const p = { part, code: i % 2 === 1, at };
    at += part.length + 1;
    return p;
  });
}
