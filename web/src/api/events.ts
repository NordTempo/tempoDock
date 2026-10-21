import { useEffect, useEffectEvent } from "react";

export type StreamOptions = {
  /** Close for good on the server's `end` event (logs, build logs). */
  finite?: boolean;
  /** The browser reconnected after a drop; events may have been missed. */
  onReconnect?: () => void;
  /** The stream is over: `end`, a problem event, or a refused connection. */
  onClose?: (error: string | null) => void;
};

// Subscribes to one of the daemon's Server-Sent Events streams while mounted
// (and `url` is set), handing each named event's parsed payload to onEvent.
// Payload types come from the spec, so callers name them:
//
//   useEventStream<LogLine>(url, ["line"], (_, line) => append(line));
//
// The daemon ends every stream with an `end` event, or an `error` event that
// carries a problem document. Finite streams close on `end`; the event bus
// stream lets the browser reconnect (it only ends when the daemon stops). An
// HTTP error response (e.g. 401) closes the stream for good.
export function useEventStream<T>(
  url: string | null,
  names: readonly string[],
  onEvent: (name: string, data: T) => void,
  { finite = false, onReconnect, onClose }: StreamOptions = {},
) {
  const dispatch = useEffectEvent((name: string, raw: string) => {
    // The spec types every event payload; JSON.parse can't check it.
    const data: T = JSON.parse(raw);
    onEvent(name, data);
  });
  const reconnected = useEffectEvent(() => onReconnect?.());
  const closed = useEffectEvent((error: string | null) => onClose?.(error));

  // Resubscribe only when the set of event names changes, not every render.
  const events = names.join(",");

  useEffect(() => {
    if (!url) return;
    const source = new EventSource(url, { withCredentials: true });
    let opened = false;
    source.addEventListener("open", () => {
      if (opened) reconnected();
      opened = true;
    });
    for (const name of events.split(",")) {
      source.addEventListener(name, (event: MessageEvent<string>) =>
        dispatch(name, event.data),
      );
    }
    source.addEventListener("end", () => {
      if (!finite) return;
      source.close();
      closed(null);
    });
    // Both the server's `error` event and connection errors arrive here.
    source.addEventListener("error", (event) => {
      if (event instanceof MessageEvent) {
        source.close();
        closed(problemDetail(event.data));
      } else if (finite || source.readyState === EventSource.CLOSED) {
        // A finite stream would replay its backlog on reconnect.
        source.close();
        closed("connection lost");
      }
    });
    return () => source.close();
  }, [url, events, finite]);
}

function problemDetail(raw: unknown) {
  try {
    const problem: unknown = typeof raw === "string" ? JSON.parse(raw) : null;
    if (typeof problem === "object" && problem && "detail" in problem) {
      return String(problem.detail);
    }
  } catch {
    // Not a problem document; fall through.
  }
  return "stream failed";
}
