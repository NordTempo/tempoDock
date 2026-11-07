import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { reconnectLive, useLiveStatus } from "../data/liveStatus";
import { Dialog } from "../ui/Dialog";
import {
  isTyping,
  type LegendItem,
  modKey,
  useKeydown,
  useLegend,
} from "./keys";

/**
 * Hosts the ? window, opened by ? anywhere outside text fields, or by
 * openKeyHelp: this page's keys, the G sequences, and the keys
 * that work everywhere.
 */
export function KeyHelpHost() {
  const [open, setOpen] = useState(false);
  useKeydown((event) => {
    if (event.key === "?" && !isTyping(event) && !event.defaultPrevented) {
      event.preventDefault();
      setOpen((o) => !o);
    }
  });
  useEffect(() => {
    const show = () => setOpen(true);
    window.addEventListener(OPEN, show);
    return () => window.removeEventListener(OPEN, show);
  }, []);
  return open ? <Help onClose={() => setOpen(false)} /> : null;
}

// Buttons and palette commands open the help through this event, so they
// need no provider of their own.
const OPEN = "dock:keys";

/** Opens the ? window from a button or a palette command. */
export const openKeyHelp = () => window.dispatchEvent(new Event(OPEN));

/**
 * Says so when the live stream is down, so a page that stopped updating
 * doesn't pass for current: retrying, or closed with a Reconnect button.
 */
export function NotLive() {
  const status = useLiveStatus();
  const queryClient = useQueryClient();
  if (status === "live") return null;
  return (
    <p
      role="status"
      className="sticky top-0 z-30 flex flex-wrap items-center gap-x-2.5 border-b border-warn-edge bg-stage-bg px-5 py-1.5 text-xs text-warn"
    >
      <span>
        <span aria-hidden>○</span> Not live:{" "}
        {status === "dropped"
          ? "lost the connection to Dock, retrying. Pages may be out of date."
          : "updates stopped. Pages may be out of date."}
      </span>
      {status === "closed" && (
        <button
          type="button"
          className="lnk text-warn underline"
          onClick={() => {
            reconnectLive();
            void queryClient.invalidateQueries();
          }}
        >
          Reconnect
        </button>
      )}
    </p>
  );
}

/**
 * The ? window: this page's keys, the G sequences that switch tabs, and
 * the keys that work everywhere, in a modal like the palette.
 */
function Help({ onClose }: { onClose: () => void }) {
  const legend = useLegend();
  const page = legend.filter((s) => !s.key.startsWith("g "));
  const go = legend.filter((s) => s.key.startsWith("g "));
  const anywhere = [
    { key: `${modKey.trim()}+K`, label: "search or run anything" },
    { key: "?", label: "show these keys" },
    { key: "esc", label: "close" },
  ];
  return (
    <Dialog
      title="Keyboard shortcuts"
      bare
      onClose={onClose}
      className="m-auto max-w-[min(640px,calc(100vw-2rem))]"
    >
      <div className="flex items-center border-b border-line px-5 py-3">
        <h2 className="text-md font-semibold">Keyboard shortcuts</h2>
        {/* The visible text is the name ("Close esc"), as WCAG 2.5.3 asks. */}
        <button
          type="button"
          className="ml-auto flex items-center gap-1.5 text-xs text-mut hover:text-fg"
          onClick={onClose}
        >
          Close <kbd>esc</kbd>
        </button>
      </div>
      {/* Focus lands here, not on the close button. */}
      <div
        data-autofocus
        tabIndex={-1}
        className="grid gap-x-8 gap-y-5 px-5 py-4 outline-none sm:grid-cols-2"
      >
        <div className="flex flex-col gap-5">
          {page.length > 0 && <Keys title="This page" items={page} />}
          <Keys title="Anywhere" items={anywhere} />
        </div>
        {go.length > 0 && <Keys title="Go to" items={go} />}
      </div>
    </Dialog>
  );
}

/** A titled list of keys; "g o" reads as G then O. */
function Keys({ title, items }: { title: string; items: LegendItem[] }) {
  return (
    <section className="flex flex-col gap-1.5">
      <h3 className="text-2xs font-medium text-mut">{title}</h3>
      <dl className="grid grid-cols-[max-content_1fr] items-baseline gap-x-4 gap-y-1.5">
        {items.map((s) => (
          <div key={`${s.key} ${s.label}`} className="contents">
            <dt className="flex items-center gap-1 text-2xs text-faint">
              {(s.key.startsWith("g ") ? s.key.split(" ") : [s.key]).map(
                (k, i) => (
                  <span key={k} className="contents">
                    {i > 0 && <span>then</span>}
                    <kbd>{k.length === 1 ? k.toUpperCase() : k}</kbd>
                  </span>
                ),
              )}
            </dt>
            <dd className="text-sec">{s.label}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}
