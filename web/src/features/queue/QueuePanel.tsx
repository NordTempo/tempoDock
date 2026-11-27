import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { canDeploy, useSession } from "../../data/queries";
import { isTyping, useKeydown } from "../../shell/keys";
import { useOrg } from "../../shell/location";
import { useNow } from "../../ui/clock";
import { plural } from "../../ui/format";
import { stepPhrase } from "../../ui/status";
import { CancelDeploy } from "../service/CancelDeploy";
import { queueQuery } from "./data";
import { closeQueue, countText } from "./open";
import { type QueueBuilder, type QueueItem, queueCount } from "./queue";

/**
 * The deploy queue's panel (open.tsx): each builder's build and the
 * releases waiting behind it in order, then the deploys past a builder,
 * with who started each, how long it has waited or run, and Cancel where
 * the reader may deploy. In the org, another org's release (or one the
 * reader can't see) is a line saying how many wait ahead; in the console
 * every org's shows, builders by name, and operators cancel any of them.
 * Times count each second only while the panel shows something in flight.
 */
export default function QueuePanel({ console }: { console: boolean }) {
  const { data, error } = useQuery(queueQuery(console));
  const panel = useRef<HTMLElement>(null);
  const builders = data?.builders ?? [];
  const deploying = data?.deploying ?? [];
  const busy = builders.length + deploying.length > 0;
  const now = useNow(busy);
  const { active, waiting } = queueCount(data);

  // Focus moves into the panel and back to where it was when it closes.
  const [previous] = useState(() => document.activeElement);
  useEffect(() => {
    panel.current?.focus();
    return () => {
      if (previous instanceof HTMLElement && previous.isConnected)
        previous.focus();
    };
  }, [previous]);
  useKeydown((event) => {
    if (event.key !== "Escape" || event.defaultPrevented) return;
    if (document.querySelector("dialog:modal")) return;
    const inside = panel.current?.contains(document.activeElement);
    if (!inside && isTyping(event)) return;
    closeQueue();
  });

  return (
    <aside
      ref={panel}
      aria-label="Deploys"
      tabIndex={-1}
      className="fixed top-(--shell-top) right-0 bottom-0 z-40 flex w-full flex-col border-l border-edge bg-page outline-none min-[900px]:w-[440px]"
    >
      <div className="flex items-center gap-2.5 border-b border-line px-3.5 py-2">
        <h2 className="font-semibold">Deploys</h2>
        <span className="min-w-0 flex-1 truncate text-xs text-mut">
          {countText(active, waiting)}
          {console && " · every organization"}
        </span>
        <button
          type="button"
          className="flex items-center gap-1.5 text-xs text-mut hover:text-fg"
          onClick={closeQueue}
        >
          Close <kbd>esc</kbd>
        </button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-3.5 pb-4">
        {error ? (
          <p className="py-3 text-fail">{error.message}</p>
        ) : !data ? null : !busy ? (
          <p className="py-3 text-mut">
            Nothing is building or waiting. A deploy shows here from the moment
            it is queued.
          </p>
        ) : (
          <>
            {builders.map((b) => (
              <Builder key={b.label} builder={b} console={console} now={now} />
            ))}
            {deploying.length > 0 && (
              <section aria-label="Past a builder">
                <h3 className="mt-4 mb-1 font-semibold">Deploying</h3>
                {deploying.slice(0, SHOWN).map((it) => (
                  <Row
                    key={key(it)}
                    item={it}
                    mark={<Dot waiting={it.status === "queued"} />}
                    console={console}
                    now={now}
                  />
                ))}
                <More count={deploying.length - SHOWN} />
              </section>
            )}
          </>
        )}
      </div>
    </aside>
  );
}

const kinds: Record<QueueBuilder["builds"], string> = {
  all: "builds every environment",
  production: "builds production",
  non_production: "builds non-production",
  none: "builds nothing now",
};

/** One builder: its build, then its line, others' runs folded to a count. */
function Builder({
  builder: b,
  console,
  now,
}: {
  builder: QueueBuilder;
  console: boolean;
  now: number;
}) {
  const line = b.waiting ?? [];
  // Others' releases in a row are one line: "2 releases of others".
  const groups: (QueueItem | number)[] = [];
  for (const it of line.slice(0, SHOWN)) {
    const last = groups.at(-1);
    if (!it.hidden) groups.push(it);
    else if (typeof last === "number") groups[groups.length - 1] = last + 1;
    else groups.push(1);
  }
  let position = 0;
  return (
    <section aria-label={b.name || b.label}>
      <h3 className="mt-4 mb-1 flex items-baseline gap-2">
        <span className="font-semibold">{b.name || b.label}</span>
        <span className="text-xs text-mut">{kinds[b.builds]}</span>
      </h3>
      {b.building ? (
        b.building.hidden ? (
          <p className={rowClass}>
            <span aria-hidden className="pt-[7px]">
              <Dot />
            </span>
            <span className="col-span-2 text-mut">
              Building a release of others, {elapsed(b.building.since, now)}
            </span>
          </p>
        ) : (
          <Row
            key={key(b.building)}
            item={b.building}
            mark={<Dot />}
            console={console}
            now={now}
            building
          />
        )
      ) : (
        <p className="py-1.5 text-mut">Idle</p>
      )}
      {groups.map((g) => {
        if (typeof g === "number") {
          position += g;
          return (
            <p key={`others-${position}`} className={rowClass}>
              <span className="font-mono text-xs text-mut">
                {position - g + 1}
                {g > 1 && `-${position}`}
              </span>
              <span className="col-span-2 text-mut">
                {plural(g, "release")} of others
              </span>
            </p>
          );
        }
        position += 1;
        return (
          <Row
            key={key(g)}
            item={g}
            mark={
              <span className="font-mono text-xs text-mut">{position}</span>
            }
            console={console}
            now={now}
          />
        );
      })}
      <More count={line.length - SHOWN} />
    </section>
  );
}

/** Rows a list shows; the rest are a count (`dock queue` lists them all). */
const SHOWN = 50;

/** The count of a list's rows past SHOWN; nothing when none are. */
function More({ count }: { count: number }) {
  if (count <= 0) return null;
  return (
    <p className="py-2 text-mut">
      {plural(count, "more release")}, listed by{" "}
      <code className="font-mono">dock queue</code>
    </p>
  );
}

const rowClass =
  "grid grid-cols-[20px_minmax(0,1fr)_auto] items-start gap-x-2.5 border-b border-line py-2";

/**
 * One release: where it is, what step it's on and for how long, who
 * started it, the push waiting behind it, and Cancel.
 */
function Row({
  item: it,
  mark,
  console,
  now,
  building,
}: {
  item: QueueItem;
  mark: ReactNode;
  console: boolean;
  now: number;
  building?: boolean;
}) {
  const org = useOrg();
  const session = useSession();
  const ref = {
    project: it.project ?? "",
    environment: it.environment ?? "",
    service: it.service ?? "",
  };
  const operator = console && session?.platform === "operator";
  const mayCancel = console ? operator : canDeploy(session, ref);
  const waits =
    !building && it.status !== "starting" && it.status !== "health_checking";
  const what = building
    ? `building ${elapsed(it.since, now)}, waited ${span(it.queued_at, it.since)}`
    : waits
      ? `${step(it)}, waiting ${elapsed(it.queued_at, now)}`
      : `${step(it)} ${elapsed(it.since, now)}`;
  const name = (
    <>
      <span className="font-medium">{it.service}</span>{" "}
      <span className="text-mut">
        {console && `${it.org} / `}
        {it.project} / {it.environment} · r{it.release}
      </span>
    </>
  );
  return (
    <div className={rowClass}>
      <span className="pt-0.5">{mark}</span>
      <span className="min-w-0">
        {console ? (
          <span className="block truncate">{name}</span>
        ) : (
          <Link
            to="/$org/$project/$environment/$service/history/$number"
            params={{ org, ...ref, number: String(it.release) }}
            onClick={closeQueue}
            className="block truncate hover:underline"
          >
            {name}
          </Link>
        )}
        <span className="block truncate text-xs text-mut">
          {what} · {by(it)}
        </span>
        {it.next && (
          <span className="block truncate text-xs text-mut">
            Then push{" "}
            <span className="font-mono">{it.next.sha.slice(0, 7)}</span>
            {it.next.message && ` ${it.next.message}`}
          </span>
        )}
      </span>
      <span>
        {mayCancel && (
          <CancelDeploy
            serviceRef={ref}
            number={it.release ?? 0}
            releaseId={console ? it.id : undefined}
          />
        )}
      </span>
    </div>
  );
}

/** A deploy's step as the queue says it. */
function step(it: QueueItem) {
  if (it.status === "building")
    // Not at a builder yet: the registry first, or an image to pull.
    return it.source === "upload"
      ? "waiting for the registry"
      : "pulling the image";
  return it.status ? stepPhrase[it.status] : "";
}

/** Who or what started it, and from what. */
function by(it: QueueItem) {
  const from = it.commit
    ? `${it.commit.sha.slice(0, 7)}${it.commit.message ? ` ${it.commit.message}` : ""}`
    : it.source === "promote" ||
        it.source === "rollback" ||
        it.source === "restart"
      ? `${it.source} of r${it.from_release}`
      : it.source;
  return `${it.created_by} · ${from}`;
}

/** The time from `iso` to now, "1:05" or "12:40" or "2:03:10". */
function elapsed(iso: string, now: number) {
  return clockSpan(now - Date.parse(iso));
}

/** The time between two instants, as elapsed says it. */
function span(from: string, to: string) {
  return clockSpan(Date.parse(to) - Date.parse(from));
}

function clockSpan(ms: number) {
  const s = Math.max(0, Math.floor(ms / 1000));
  const pad = (n: number) => String(n).padStart(2, "0");
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return h > 0 ? `${h}:${pad(m)}:${pad(s % 60)}` : `${m}:${pad(s % 60)}`;
}

/** A dot: filled while it runs, an outline while it waits. */
function Dot({ waiting }: { waiting?: boolean }) {
  return (
    <span
      aria-hidden
      className={`inline-block size-[7px] rounded-full ${waiting ? "border border-mut" : "bg-warn"}`}
    />
  );
}

/** A release's key in the queue. */
const key = (it: QueueItem) =>
  `${it.org ?? ""}/${it.project}/${it.environment}/${it.service}/${it.release}`;
