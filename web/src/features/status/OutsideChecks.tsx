import { useSuspenseQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { useIsAdmin } from "../../data/queries";
import {
  Act,
  Button,
  Confirm,
  ErrorText,
  MoreItem,
  MoreMenu,
  Scroll,
  StateText,
} from "../../ui/kit";
import {
  CheckUrl,
  checkState,
  IntervalEdit,
  LastProbe,
  percent,
  StatusEdit,
  Strip,
} from "../uptime/UptimePanel";
import {
  type OutsideCheck,
  statusQueries,
  useAddOutsideCheck,
  useRemoveOutsideCheck,
  useUpdateOutsideCheck,
} from "./data";

// The organization's checks of sites outside Dock (ADR 0055), under its
// status pages: what components show besides Dock's own services. Admins
// add, change and remove them; the component dialog adds them too. Live
// state comes from uptime events (data/live.ts).

/**
 * A value as one shell argument for a CLI hint: as it is when it is
 * plain, else in single quotes ("?a=1&b=2" would end the command).
 */
export const shellArg = (v: string) =>
  /^[\w@%+=:,./-]+$/.test(v) ? v : `'${v.replaceAll("'", "'\\''")}'`;

/** Outside checks are probed at most once a minute. */
const floor = 60;

const outsideIntervals = [
  [60, "1m"],
  [300, "5m"],
  [900, "15m"],
  [3600, "1h"],
] as const;

/** The Outside checks section of Status. */
export function OutsideChecks() {
  const admin = useIsAdmin();
  const checks = useSuspenseQuery(statusQueries.outside()).data;
  const [adding, setAdding] = useState(false);
  // The check added here, marked until the page changes.
  const [added, setAdded] = useState<number | null>(null);
  return (
    <section aria-labelledby="outside-checks" className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <h2 id="outside-checks" className="font-semibold">
          Outside checks
        </h2>
        <span className="text-mut">
          Sites outside Dock, for components; down alerts go to your channels.
        </span>
        {admin && !adding && (
          <span className="ml-auto">
            <Button onClick={() => setAdding(true)}>Check a URL</Button>
          </span>
        )}
      </div>
      <datalist id="outside-check-statuses">
        <option value="200" />
        <option value="2xx" />
        <option value="3xx" />
      </datalist>
      {adding && (
        <AddOutside
          onAdded={(id) => {
            setAdded(id);
            setAdding(false);
          }}
          onCancel={() => setAdding(false)}
        />
      )}
      {checks.length === 0 && !adding && <p className="text-mut">None.</p>}
      {checks.length > 0 && (
        <Scroll>
          <table className="w-full">
            <thead>
              <tr>
                <th>State</th>
                <th>URL</th>
                <th>Every</th>
                <th>Expects</th>
                <th>Last 24 hours</th>
                <th className="text-right">24h</th>
                <th className="text-right">30d</th>
                <th>Last probe</th>
                <th>Used by</th>
                <th>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {checks.map((c) => (
                <OutsideRow
                  key={c.id}
                  check={c}
                  admin={admin}
                  fresh={c.id === added}
                />
              ))}
            </tbody>
          </table>
        </Scroll>
      )}
    </section>
  );
}

/** "acme / Vendor, status / API": the components a check feeds. */
const usedBy = (c: OutsideCheck) =>
  c.used_by.map((u) => `${u.page} / ${u.component}`).join(", ");

function OutsideRow({
  check: c,
  admin,
  fresh,
}: {
  check: OutsideCheck;
  admin: boolean;
  fresh: boolean;
}) {
  const remove = useRemoveOutsideCheck();
  const [confirming, setConfirming] = useState(false);
  const p = c.last_probe;
  const cli = `dock uptime remove ${c.id} --outside`;
  return (
    <tr className={fresh ? "[&>td]:bg-sel" : ""}>
      <td>
        <StateText state={checkState(c.state)} />
      </td>
      <td className="whitespace-nowrap" title={c.url}>
        <CheckUrl url={c.url} />
      </td>
      <td className="whitespace-nowrap">
        {admin ? (
          <IntervalEdit
            check={c}
            useUpdate={useUpdateOutsideCheck}
            target="--outside"
            floor={floor}
          />
        ) : (
          <span className="font-mono">{c.interval_seconds}s</span>
        )}
      </td>
      <td className="whitespace-nowrap">
        {admin ? (
          <StatusEdit
            check={c}
            useUpdate={useUpdateOutsideCheck}
            target="--outside"
          />
        ) : (
          <span className="font-mono">{c.expect_status}</span>
        )}
      </td>
      <td>
        <Strip series={c.series} />
      </td>
      <td className="text-right font-mono">{percent(c.uptime_24h)}</td>
      <td className="text-right font-mono">{percent(c.uptime_30d)}</td>
      <td
        className={`min-w-[120px] whitespace-normal ${p && !p.up ? "text-fail" : "text-mut"}`}
      >
        <LastProbe check={c} />
      </td>
      <td className="w-full min-w-[140px] whitespace-normal text-sec">
        {usedBy(c) || <span className="text-mut">not used</span>}
      </td>
      <td className="w-px py-1 text-right whitespace-nowrap">
        {admin && (
          <MoreMenu label={`Check of ${c.url}`}>
            {(close) => (
              <MoreItem
                close={close}
                danger
                cli={cli}
                onClick={() => setConfirming(true)}
              >
                Remove check…
              </MoreItem>
            )}
          </MoreMenu>
        )}
        {confirming && (
          <Confirm
            danger
            action="Remove check"
            title={`Remove the check of ${c.url}?`}
            cli={cli}
            pending={remove.isPending}
            error={remove.error}
            onConfirm={() =>
              remove.mutate(c, { onSuccess: () => setConfirming(false) })
            }
            onCancel={() => {
              remove.reset();
              setConfirming(false);
            }}
          >
            Dock stops probing it, and its history goes with it.
            {c.used_by.length > 0 &&
              ` These components no longer show it: ${usedBy(c)}.`}
          </Confirm>
        )}
      </td>
    </tr>
  );
}

/** One height for the add form's inputs and buttons. */
const control = "h-[34px]";

/** The add form, in place above the table. */
function AddOutside({
  onAdded,
  onCancel,
}: {
  onAdded: (id: number) => void;
  onCancel: () => void;
}) {
  const add = useAddOutsideCheck();
  const [url, setUrl] = useState("");
  const [status, setStatus] = useState("200");
  const [intervalS, setIntervalS] = useState(60);
  // The form opens from Check a URL, so its first field takes the focus.
  const urlField = useRef<HTMLInputElement>(null);
  useEffect(() => urlField.current?.focus(), []);
  const typed = url.trim();
  return (
    <form
      aria-label="Check a URL outside Dock"
      className="flex flex-wrap items-end gap-2.5"
      onKeyDown={(e) => e.key === "Escape" && onCancel()}
      onSubmit={(e) => {
        e.preventDefault();
        add.mutate(
          { url: typed, expect_status: status, interval_seconds: intervalS },
          { onSuccess: (c) => onAdded(c.id) },
        );
      }}
    >
      <label className="flex flex-col gap-1">
        <span className="text-xs text-sec">URL</span>
        <input
          ref={urlField}
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          placeholder="https://status.example.com/"
          className={`field w-[320px] max-w-full font-mono ${control}`}
        />
      </label>
      <label
        className="flex flex-col gap-1"
        title="The answer that counts as up"
      >
        <span className="text-xs text-sec">Up when it answers</span>
        <input
          value={status}
          onChange={(e) => setStatus(e.target.value.trim())}
          list="outside-check-statuses"
          className={`field w-[96px] font-mono ${control}`}
        />
      </label>
      <label className="flex flex-col gap-1">
        <span className="text-xs text-sec">Every</span>
        <select
          value={intervalS}
          onChange={(e) => setIntervalS(Number(e.target.value))}
          className={`field min-w-[96px] ${control}`}
        >
          {outsideIntervals.map(([s, label]) => (
            <option key={s} value={s}>
              {label}
            </option>
          ))}
        </select>
      </label>
      <Act
        primary
        type="submit"
        className={control}
        cli={`dock uptime add ${typed ? shellArg(typed) : "<url>"} --outside --status ${shellArg(status)} --interval ${intervalS}s`}
        disabled={!typed || add.isPending}
        reason={typed ? undefined : "type a URL first"}
      >
        Check a URL
      </Act>
      <Button className={control} onClick={onCancel}>
        Cancel
      </Button>
      <ErrorText error={add.error} />
    </form>
  );
}
