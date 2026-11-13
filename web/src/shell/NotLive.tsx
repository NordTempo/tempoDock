import { useQueryClient } from "@tanstack/react-query";
import { reconnectLive, useLiveStatus } from "../data/liveStatus";

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
