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

// Alerts and alert channels (ADR 0020). An org's alerts (its projects and
// services) are for its members and its channels; the platform's (servers,
// connections, Dock itself) for operators, on the console (ADR 0062).
// Open alerts stay live through `alert` events (applyAlertEvent); nothing
// polls.

type Schemas = components["schemas"];
export type Alert = Schemas["Alert"];
export type AlertChannel = Schemas["AlertChannel"];
export type NewAlertChannel = Schemas["CreateAlertChannelRequest"];
export type Severity = Alert["severity"];
/** Which new vulnerabilities in live images alert (ADR 0074). */
export type VulnAlerts = Schemas["VulnAlertSettings"]["alert"];
/** Whose alerts: the current org's, or the platform's. */
export type AlertScope = "org" | "platform";

/** Alerts in the history the page loads; live events keep it at this. */
const HISTORY_LIMIT = 100;

/** The API's query for a scope: platform=true for the platform's. */
export const scopeQuery = (scope: AlertScope) =>
  scope === "platform" ? { platform: true } : {};

export const alertKeys = {
  /** Open alerts, as the API orders them. */
  open: (scope: AlertScope) => ["alerts", scope, "open"] as const,
  /** Every alert, newest first. */
  all: (scope: AlertScope) => ["alerts", scope, "all"] as const,
  channels: (scope: AlertScope) => ["alerts", scope, "channels"] as const,
  vulns: ["alerts", "org", "vulnerabilities"] as const,
};

export const alertQueries = {
  vulns: () =>
    queryOptions({
      queryKey: alertKeys.vulns,
      queryFn: () => body(api.GET("/api/v1/alerts/vulnerabilities")),
    }),
  open: (scope: AlertScope) =>
    queryOptions({
      queryKey: alertKeys.open(scope),
      queryFn: () =>
        list(
          api.GET("/api/v1/alerts", {
            params: { query: scopeQuery(scope) },
          }),
        ),
    }),
  all: (scope: AlertScope) =>
    queryOptions({
      queryKey: alertKeys.all(scope),
      queryFn: () =>
        list(
          api.GET("/api/v1/alerts", {
            params: {
              query: { ...scopeQuery(scope), all: true, limit: HISTORY_LIMIT },
            },
          }),
        ),
    }),
  channels: (scope: AlertScope) =>
    queryOptions({
      queryKey: alertKeys.channels(scope),
      queryFn: () =>
        list(
          api.GET("/api/v1/alerts/channels", {
            params: { query: scopeQuery(scope) },
          }),
        ),
    }),
};

export const useOpenAlerts = (scope: AlertScope) =>
  useSuspenseQuery(alertQueries.open(scope)).data;
export const useAllAlerts = (scope: AlertScope) =>
  useSuspenseQuery(alertQueries.all(scope)).data;
export const useAlertChannels = (scope: AlertScope) =>
  useSuspenseQuery(alertQueries.channels(scope)).data;

/**
 * The org's open alerts, for its members (the sidebar, a project's
 * problems); empty while loading or when the caller is in no org.
 */
export function useOrgOpenAlerts(): Alert[] {
  return useQuery(alertQueries.open("org")).data ?? [];
}

/** The platform's open alerts for operators (the console); else empty. */
export function usePlatformOpenAlerts(): Alert[] {
  const operator = useIsOperator();
  return (
    useQuery({ ...alertQueries.open("platform"), enabled: operator }).data ?? []
  );
}

const rank = (s: Severity) => (s === "critical" ? 0 : 1);

/** Critical first, then newest first, as the API orders open alerts. */
export const byUrgency = (a: Alert, b: Alert) =>
  rank(a.severity) - rank(b.severity) || b.id - a.id;

/**
 * An alert opened, changed or resolved: patch its scope's open list and
 * full history, where cached.
 */
export function applyAlertEvent(queryClient: QueryClient, alert: Alert) {
  const scope: AlertScope = alert.platform ? "platform" : "org";
  queryClient.setQueryData<Alert[]>(alertKeys.open(scope), (list) => {
    if (!list) return list;
    const rest = list.filter((a) => a.id !== alert.id);
    return alert.resolved_at ? rest : [...rest, alert].sort(byUrgency);
  });
  queryClient.setQueryData<Alert[]>(alertKeys.all(scope), (list) =>
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

export const useVulnAlerts = () =>
  useSuspenseQuery(alertQueries.vulns()).data.alert;

export const useSetVulnAlerts = () =>
  useAlertAction((alert: VulnAlerts) =>
    body(api.PUT("/api/v1/alerts/vulnerabilities", { body: { alert } })),
  );

export const useResolveAlert = () =>
  useAlertAction((id: number) =>
    body(api.POST("/api/v1/alerts/{id}/resolve", { params: { path: { id } } })),
  );

export const useCreateAlertChannel = (scope: AlertScope) =>
  useAlertAction((input: NewAlertChannel) =>
    body(
      api.POST("/api/v1/alerts/channels", {
        params: { query: scopeQuery(scope) },
        body: input,
      }),
    ),
  );

export const useRemoveAlertChannel = (scope: AlertScope) =>
  useAlertAction((channel: string) =>
    done(
      api.DELETE("/api/v1/alerts/channels/{channel}", {
        params: { path: { channel }, query: scopeQuery(scope) },
      }),
    ),
  );

export const useTestAlertChannel = (scope: AlertScope) =>
  useAlertAction((channel: string) =>
    body(
      api.POST("/api/v1/alerts/channels/{channel}/test", {
        params: { path: { channel }, query: scopeQuery(scope) },
      }),
    ),
  );
