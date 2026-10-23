import {
  type QueryClient,
  queryOptions,
  useMutation,
  useQuery,
  useQueryClient,
  useSuspenseQuery,
} from "@tanstack/react-query";
import { api, body, done, list } from "../../api/client";
import type { components } from "../../api/schema";
import { useIsOperator } from "../../data/queries";

// Alerts and alert channels (ADR 0020), for admins. Open alerts stay live
// through `alert` events (applyAlertEvent); nothing polls.

type Schemas = components["schemas"];
export type Alert = Schemas["Alert"];
export type AlertChannel = Schemas["AlertChannel"];
export type NewAlertChannel = Schemas["CreateAlertChannelRequest"];
export type Severity = Alert["severity"];

/** Alerts in the history the page loads; live events keep it at this. */
const HISTORY_LIMIT = 100;

export const alertKeys = {
  /** Open alerts, as the API orders them. */
  open: ["alerts", "open"],
  /** Every alert, newest first. */
  all: ["alerts", "all"],
  channels: ["alerts", "channels"],
} as const;

export const alertQueries = {
  open: () =>
    queryOptions({
      queryKey: alertKeys.open,
      queryFn: () => list(api.GET("/api/v1/alerts")),
    }),
  all: () =>
    queryOptions({
      queryKey: alertKeys.all,
      queryFn: () =>
        list(
          api.GET("/api/v1/alerts", {
            params: { query: { all: true, limit: HISTORY_LIMIT } },
          }),
        ),
    }),
  channels: () =>
    queryOptions({
      queryKey: alertKeys.channels,
      queryFn: () => list(api.GET("/api/v1/alerts/channels")),
    }),
};

export const useOpenAlerts = () => useSuspenseQuery(alertQueries.open()).data;
export const useAllAlerts = () => useSuspenseQuery(alertQueries.all()).data;
export const useAlertChannels = () =>
  useSuspenseQuery(alertQueries.channels()).data;

/**
 * Open alerts where admins can see them (the top bar, overview problems);
 * empty for everyone else, who can't read alerts.
 */
export function useOpenAlertsIfAdmin(): Alert[] {
  const operator = useIsOperator();
  return useQuery({ ...alertQueries.open(), enabled: operator }).data ?? [];
}

const rank = (s: Severity) => (s === "critical" ? 0 : 1);

/** Critical first, then newest first, as the API orders open alerts. */
export const byUrgency = (a: Alert, b: Alert) =>
  rank(a.severity) - rank(b.severity) || b.id - a.id;

/**
 * An alert opened, changed or resolved: patch the open list and the full
 * history, where cached.
 */
export function applyAlertEvent(queryClient: QueryClient, alert: Alert) {
  queryClient.setQueryData<Alert[]>(alertKeys.open, (list) => {
    if (!list) return list;
    const rest = list.filter((a) => a.id !== alert.id);
    return alert.resolved_at ? rest : [...rest, alert].sort(byUrgency);
  });
  queryClient.setQueryData<Alert[]>(alertKeys.all, (list) =>
    list
      ? [alert, ...list.filter((a) => a.id !== alert.id)]
          .sort((a, b) => b.id - a.id)
          .slice(0, HISTORY_LIMIT)
      : list,
  );
}

function useAlertAction<Vars, Data>(mutationFn: (vars: Vars) => Promise<Data>) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn,
    onSettled: () =>
      Promise.all(
        [["alerts"], ["audit"]].map((queryKey) =>
          queryClient.invalidateQueries({ queryKey }),
        ),
      ),
  });
}

export const useResolveAlert = () =>
  useAlertAction((id: number) =>
    body(api.POST("/api/v1/alerts/{id}/resolve", { params: { path: { id } } })),
  );

export const useCreateAlertChannel = () =>
  useAlertAction((input: NewAlertChannel) =>
    body(api.POST("/api/v1/alerts/channels", { body: input })),
  );

export const useRemoveAlertChannel = () =>
  useAlertAction((channel: string) =>
    done(
      api.DELETE("/api/v1/alerts/channels/{channel}", {
        params: { path: { channel } },
      }),
    ),
  );

export const useTestAlertChannel = () =>
  useAlertAction((channel: string) =>
    body(
      api.POST("/api/v1/alerts/channels/{channel}/test", {
        params: { path: { channel } },
      }),
    ),
  );
