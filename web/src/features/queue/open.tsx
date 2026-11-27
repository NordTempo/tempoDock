import { useQuery } from "@tanstack/react-query";
import { lazy, Suspense, useEffect, useSyncExternalStore } from "react";
import { Icon } from "../../ui/icons";
import { closeHelp, useHelpOpen } from "../help/open";
import { queueQuery } from "./data";
import { queueCount } from "./queue";

/**
 * The deploy queue's panel (Saile's pick B, 2026-11-27): a count in the
 * org's top bar (and a row in the console's sidebar) that opens a column
 * at the right edge, where Help opens, listing what each builder builds
 * and what waits. One of the two shows at a time. The state lives outside
 * React, as help's does, so the bar, the palette and the console open it
 * without a provider.
 */
let open = false;
const listeners = new Set<() => void>();

function set(next: boolean) {
  open = next;
  for (const listener of listeners) listener();
}

const subscribe = (listener: () => void) => {
  listeners.add(listener);
  return () => listeners.delete(listener);
};

/** Opens the queue's panel, closing help's. */
export const openQueue = () => {
  closeHelp();
  set(true);
};

export const closeQueue = () => set(false);

/** Whether the queue's panel is open. */
export const useQueueOpen = () => useSyncExternalStore(subscribe, () => open);

// The panel loads on the first open, not with the shell.
const QueuePanel = lazy(() => import("./QueuePanel"));

/**
 * Mounts the queue's panel while open, the org's or (in the console)
 * every org's. Help opening closes it. The shell mounts it once.
 */
export function QueueHost({ console }: { console: boolean }) {
  const shown = useQueueOpen();
  const help = useHelpOpen();
  useEffect(() => {
    if (help) closeQueue();
  }, [help]);
  // Wide windows make room for the panel instead of covering the page.
  useEffect(() => {
    document.documentElement.classList.toggle("queue-open", shown);
  }, [shown]);
  if (!shown) return null;
  return (
    <Suspense fallback={null}>
      <QueuePanel console={console} />
    </Suspense>
  );
}

/** "2 deploying · 3 waiting", or "" while nothing is. */
export function countText(active: number, waiting: number) {
  return [
    active > 0 && `${active} deploying`,
    waiting > 0 && `${waiting} waiting`,
  ]
    .filter(Boolean)
    .join(" · ");
}

/**
 * The top bar's count of the org's releases deploying and waiting: shown
 * only while there are some, and it opens the panel. The queue is read
 * once and kept live by events (data.ts), so the count never polls.
 */
export function DeployCount() {
  const { data } = useQuery(queueQuery(false));
  const shown = useQueueOpen();
  const { active, waiting } = queueCount(data);
  if (active + waiting === 0) return null;
  return (
    <button
      type="button"
      aria-expanded={shown}
      title="What deploys and what waits for a builder"
      onClick={() => (shown ? closeQueue() : openQueue())}
      className="flex min-h-[28px] items-center gap-2 rounded-ctl border border-line px-2.5 text-sec hover:border-edge hover:text-fg aria-expanded:bg-sel aria-expanded:text-fg"
    >
      <span aria-hidden className="size-[7px] rounded-full bg-warn" />
      <span className="hidden sm:inline">{countText(active, waiting)}</span>
      <span className="sm:hidden">
        {active + waiting}
        <span className="sr-only"> deploying or waiting</span>
      </span>
    </button>
  );
}

/**
 * The console sidebar's row for every org's queue: its count, and it
 * opens the panel there. The console has no top bar, so the row stands
 * where its other pages are listed.
 */
export function ConsoleDeploys({ className }: { className: string }) {
  const { data } = useQuery(queueQuery(true));
  const shown = useQueueOpen();
  const { active, waiting } = queueCount(data);
  return (
    <button
      type="button"
      aria-expanded={shown}
      onClick={() => (shown ? closeQueue() : openQueue())}
      className={className}
    >
      <Icon name="list" className="text-mut" />
      <span className="flex-1 text-left">Deploys</span>
      {active + waiting > 0 && (
        <span className="text-2xs text-warn">
          {active}
          {waiting > 0 && ` · ${waiting}`}
          <span className="sr-only"> deploying or waiting</span>
        </span>
      )}
    </button>
  );
}
