import { useEffect, useEffectEvent } from "react";
import { withOrg } from "./client";

export type StreamOptions = {
  /** Close for good on the server's `end` event (logs, build logs). */
  finite?: boolean;
  /** The server is subscribed; true after a connection was lost. */
  onReady?: (reconnected: boolean) => void;
  /** The browser reconnected after a drop; events may have been missed. */
  onReconnect?: () => void;
  /** The connection dropped; the browser is retrying (open streams only). */
  onDrop?: () => void;
  /**
   * A finite stream lost its connection and is about to open again, which
   * replays its backlog: drop what was collected. Without it a lost
   * finite stream just closes.
   */
  onRestart?: () => void;
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
// HTTP error response (e.g. 401) closes the stream for good. A finite
// stream with onRestart opens again after a lost connection, backing off
// from 1 s to 30 s, and gives up after RETRIES tries in a row
// (docs/performance.md lists the timer).
export function useEventStream<T>(
  url: string | null,
  names: readonly string[],
  onEvent: (name: string, data: T) => void,
  {
    finite = false,
    onReady,
    onReconnect,
    onDrop,
    onRestart,
    onClose,
  }: StreamOptions = {},
) {
  const dispatch = useEffectEvent((name: string, raw: string) => {
    // The spec types every event payload; JSON.parse can't check it.
    const data: T = JSON.parse(raw);
    onEvent(name, data);
  });
  const ready = useEffectEvent((again: boolean) => onReady?.(again));
  const reconnected = useEffectEvent(() => onReconnect?.());
  const dropped = useEffectEvent(() => onDrop?.());
  const restart = useEffectEvent(() => onRestart?.());
  const retries = !!onRestart;
  const closed = useEffectEvent((error: string | null) => onClose?.(error));

  // Resubscribe only when the set of event names changes, not every render.
  const events = names.join(",");

  useEffect(() => {
    if (!url) return;
    let source: EventSource;
    let timer: ReturnType<typeof setTimeout> | undefined;
    // Failed tries in a row; a `ready` event resets it.
    let tries = 0;
    let opened = false;
    let wasReady = false;
    const connect = () => {
      source = new EventSource(withOrg(url), { withCredentials: true });
      source.addEventListener("ready", () => {
        tries = 0;
        ready(wasReady);
        wasReady = true;
      });
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
        } else if (finite && retries && tries < RETRIES) {
          // A finite stream would replay its backlog on the browser's own
          // reconnect; start over cleanly instead.
          source.close();
          timer = setTimeout(
            () => {
              restart();
              connect();
            },
            Math.min(30_000, 1000 * 2 ** tries++),
          );
        } else if (finite || source.readyState === EventSource.CLOSED) {
          source.close();
          closed("connection lost");
        } else {
          dropped();
        }
      });
    };
    connect();
    return () => {
      clearTimeout(timer);
      source.close();
    };
  }, [url, events, finite, retries]);
}

/** Tries in a row before a lost finite stream gives up (about a minute). */
const RETRIES = 6;

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
