import { type QueryClient, useQueryClient } from "@tanstack/react-query";
import { useEffect, useEffectEvent } from "react";

// Handlers keyed by SSE event name. Payloads are parsed JSON, typed `unknown`
// until the API spec describes its events; narrow them in the handler.
export type EventHandlers = Record<
  string,
  (data: unknown, queryClient: QueryClient) => void
>;

// Subscribes to the daemon's event stream while mounted and hands each event
// to its handler with the QueryClient, so live data flows into the query
// cache instead of being polled:
//
//   useServerEvents({
//     "service.status": (data, qc) => qc.invalidateQueries({ queryKey: [...] }),
//   });
//
// The browser reconnects dropped connections on its own; after a reconnect
// every query is invalidated, since events may have been missed in the gap.
// An HTTP error response (e.g. 401) closes the stream for good.
export function useServerEvents(
  handlers: EventHandlers,
  url = "/api/v1/events",
) {
  const queryClient = useQueryClient();
  const dispatch = useEffectEvent((name: string, raw: string) => {
    const data: unknown = JSON.parse(raw);
    handlers[name]?.(data, queryClient);
  });
  const resync = useEffectEvent(() => queryClient.invalidateQueries());

  // Resubscribe only when the set of event names changes, not every render.
  const names = Object.keys(handlers).sort().join(",");

  useEffect(() => {
    if (!names) return;
    const source = new EventSource(url, { withCredentials: true });
    let connected = false;
    source.addEventListener("open", () => {
      if (connected) resync();
      connected = true;
    });
    for (const name of names.split(",")) {
      source.addEventListener(name, (event: MessageEvent<string>) =>
        dispatch(name, event.data),
      );
    }
    return () => source.close();
  }, [url, names]);
}
