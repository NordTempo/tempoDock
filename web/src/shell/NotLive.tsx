import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import {
  type LiveStatus,
  reconnectLive,
  useLiveStatus,
} from "../data/liveStatus";

/** How long the stream is down before the strip shows: a blip says nothing. */
export const NOT_LIVE_DELAY = 2000;

/**
 * Says so when the live stream is down, so a page that stopped updating
 * doesn't pass for current: retrying, or closed with a Reconnect button.
 * It sits in the shell's sticky stack under the top bar (Shell.tsx) and
 * pushes the page down: one line of a fixed 28px, which index.css counts
 * in --shell-top through the `not-live` class. Narrow windows get the
 * short words so it never wraps.
 */
export function NotLive() {
  const status = useShownStatus();
  const queryClient = useQueryClient();
  if (status === "live") return null;
  return (
    <p
      role="status"
      className="not-live flex h-[28px] items-center gap-2.5 overflow-hidden border-b border-warn-edge bg-stage-bg px-4 text-xs whitespace-nowrap text-warn sm:px-5"
    >
      <span className="min-w-0 truncate">
        <span aria-hidden>○</span> Not live
        <span className="text-warn-text sm:hidden">
          {status === "dropped" ? ", retrying." : "."}
        </span>
        <span className="hidden text-warn-text sm:inline">
          {status === "dropped"
            ? ": lost the connection to Dock, retrying. Pages may be out of date."
            : ": updates stopped. Pages may be out of date."}
        </span>
      </span>
      {status === "closed" && (
        <button
          type="button"
          className="lnk text-warn-text underline"
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
 * The live status, but "live" until the stream has been down for
 * NOT_LIVE_DELAY: a one-shot timer per drop (docs/performance.md), so a
 * restart of Dock or a network blip doesn't flash the strip. Back to live
 * clears it at once.
 */
function useShownStatus(): LiveStatus {
  const status = useLiveStatus();
  const down = status !== "live";
  const [late, setLate] = useState(false);
  useEffect(() => {
    if (!down) return;
    const timer = setTimeout(() => setLate(true), NOT_LIVE_DELAY);
    return () => {
      clearTimeout(timer);
      setLate(false);
    };
  }, [down]);
  return down && late ? status : "live";
}
