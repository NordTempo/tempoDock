import { queryOptions } from "@tanstack/react-query";
import { api, body, done, list } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import type { State } from "../../ui/status";

// The organization's public status pages (ADR 0073). What a page says is
// worked out on the server from uptime checks, alerts and sleep, so
// data/live.ts refetches these on uptime, alert and sleep events and on
// status_page.* and incident.* audit events. A page carries its incidents
// of the last 90 days, so an incident's screen reads its page.

type Schema = components["schemas"];
type PageSchema = Schema["StatusPage"];
type ComponentSchema = Schema["StatusComponent"];

/** A component, with the lists the API may send as null as arrays. */
export type StatusComponent = Omit<ComponentSchema, "days" | "sources"> & {
  days: NonNullable<ComponentSchema["days"]>;
  sources: NonNullable<ComponentSchema["sources"]>;
};
type IncidentSchema = Schema["StatusIncident"];
/** An incident, with the lists the API may send as null as arrays. */
export type StatusIncident = Omit<IncidentSchema, "components" | "updates"> & {
  components: StatusImpact[];
  updates: NonNullable<IncidentSchema["updates"]>;
};
export type StatusImpact = Schema["StatusImpact"];
export type IncidentState = IncidentSchema["state"];
/** The states an update may post. */
export type PostState = Schema["PostIncidentUpdateRequest"]["state"];
type HostnameSchema = Schema["StatusHostname"];
/** A page's custom hostname, its records as an array. */
type StatusHostname = Omit<HostnameSchema, "records"> & {
  records: NonNullable<HostnameSchema["records"]>;
};
/** A page with its components, incidents and custom hostname. */
export type StatusPage = Omit<
  PageSchema,
  "components" | "incidents" | "hostname"
> & {
  components: StatusComponent[];
  incidents: StatusIncident[];
  hostname?: StatusHostname;
};
export type StatusComponentInput = Schema["StatusComponentInput"];
export type StatusSourceRef = Schema["StatusSourceRef"];
export type ComponentStatus = PageSchema["status"];
type PagePatch = Schema["UpdateStatusPageRequest"];

const statusKeys = {
  all: ["status-pages"],
  hooks: (slug: string) => ["status-pages", "hooks", slug],
  list: ["status-pages", "list"],
  page: (slug: string) => ["status-pages", "page", slug],
  sources: ["status-pages", "sources"],
};

/** The API's incident, its lists as arrays. */
const toIncident = (i: IncidentSchema): StatusIncident => ({
  ...i,
  components: i.components ?? [],
  updates: i.updates ?? [],
});

/** The API's page, its lists as arrays. */
const toPage = (p: PageSchema): StatusPage => ({
  ...p,
  components: (p.components ?? []).map((c) => ({
    ...c,
    days: c.days ?? [],
    sources: c.sources ?? [],
  })),
  incidents: (p.incidents ?? []).map(toIncident),
  hostname: p.hostname
    ? { ...p.hostname, records: p.hostname.records ?? [] }
    : undefined,
});

export const statusQueries = {
  list: () =>
    queryOptions({
      queryKey: statusKeys.list,
      queryFn: () => list(api.GET("/api/v1/status-pages")),
    }),
  page: (slug: string) =>
    queryOptions({
      queryKey: statusKeys.page(slug),
      queryFn: () =>
        body(
          api.GET("/api/v1/status-pages/{page}", {
            params: { path: { page: slug } },
          }),
        ).then(toPage),
    }),
  /** A page's update hooks (admins). */
  hooks: (slug: string) =>
    queryOptions({
      queryKey: statusKeys.hooks(slug),
      queryFn: () =>
        list(
          api.GET("/api/v1/status-pages/{page}/hooks", {
            params: { path: { page: slug } },
          }),
        ),
    }),
  /** What components may be built from: services and their checks. */
  sources: () =>
    queryOptions({
      queryKey: statusKeys.sources,
      queryFn: () =>
        list(api.GET("/api/v1/status-sources")).then((rows) =>
          rows.map((r) => ({ ...r, checks: r.checks ?? [] })),
        ),
    }),
};

export const useCreateStatusPage = () =>
  useAction(
    (req: Schema["CreateStatusPageRequest"]) =>
      body(api.POST("/api/v1/status-pages", { body: req })).then(toPage),
    () => [statusKeys.all],
    (_, p) => `Made status page ${p.slug}`,
  );

export const useUpdateStatusPage = (slug: string) =>
  useAction(
    (patch: PagePatch) =>
      body(
        api.PATCH("/api/v1/status-pages/{page}", {
          params: { path: { page: slug } },
          body: patch,
        }),
      ).then(toPage),
    () => [statusKeys.all],
    (patch) =>
      patch.published === true
        ? `Published ${slug}`
        : patch.published === false
          ? `Unpublished ${slug}`
          : `Saved ${slug}`,
  );

export const useCheckStatusHostname = (slug: string) =>
  useAction(
    () =>
      body(
        api.POST("/api/v1/status-pages/{page}/hostname/check", {
          params: { path: { page: slug } },
        }),
      ).then(toPage),
    () => [statusKeys.all],
    (_, p) =>
      p.hostname?.verified
        ? `${p.hostname.hostname} is verified`
        : "The TXT record isn't there yet",
  );

export const useDeleteStatusPage = (slug: string) =>
  useAction(
    () =>
      done(
        api.DELETE("/api/v1/status-pages/{page}", {
          params: { path: { page: slug } },
        }),
      ),
    () => [statusKeys.list],
    () => `Removed status page ${slug}`,
  );

export const useSetStatusComponents = (slug: string) =>
  useAction(
    (components: StatusComponentInput[]) =>
      body(
        api.PUT("/api/v1/status-pages/{page}/components", {
          params: { path: { page: slug } },
          body: { components },
        }),
      ).then(toPage),
    () => [statusKeys.all],
  );

/** A page's components as the list the API takes back. */
export const toInputs = (p: StatusPage): StatusComponentInput[] =>
  p.components.map((c) => ({
    id: c.id,
    name: c.name,
    group: c.group,
    description: c.description,
    auto: c.auto,
    sources: c.sources.map(({ service, check }) =>
      check ? { check } : { service },
    ),
  }));

/** How a status reads in the editor: the words and a tinted state. */
export const statusState: Record<ComponentStatus, State> = {
  operational: { tone: "ok", glyph: "●", label: "operational" },
  idle: { tone: "info", glyph: "◌", label: "idle" },
  maintenance: { tone: "info", glyph: "◐", label: "maintenance" },
  degraded: { tone: "warn", glyph: "▲", label: "degraded" },
  partial_outage: { tone: "warn", glyph: "▲", label: "partial outage" },
  major_outage: { tone: "fail", glyph: "✕", label: "major outage" },
};

export const useOpenIncident = (slug: string) =>
  useAction(
    (req: Schema["OpenIncidentRequest"]) =>
      body(
        api.POST("/api/v1/status-pages/{page}/incidents", {
          params: { path: { page: slug } },
          body: req,
        }),
      ).then(toIncident),
    () => [statusKeys.all],
    (_, i) =>
      i.state === "draft" ? `Saved draft ${i.title}` : `Opened ${i.title}`,
  );

/** What a toast says after an update that moves an incident or window on. */
const postedText: Partial<Record<IncidentState, string>> = {
  resolved: "Resolved",
  in_progress: "Started",
  completed: "Completed",
  cancelled: "Cancelled",
};

export const useScheduleMaintenance = (slug: string) =>
  useAction(
    (req: Schema["ScheduleMaintenanceRequest"]) =>
      body(
        api.POST("/api/v1/status-pages/{page}/maintenance", {
          params: { path: { page: slug } },
          body: req,
        }),
      ).then(toIncident),
    () => [statusKeys.all],
    (_, w) => `Scheduled ${w.title}`,
  );

export const usePostIncidentUpdate = (id: string) =>
  useAction(
    (req: Schema["PostIncidentUpdateRequest"]) =>
      body(
        api.POST("/api/v1/incidents/{incident}/updates", {
          params: { path: { incident: id } },
          body: req,
        }),
      ).then(toIncident),
    () => [statusKeys.all],
    (req) => postedText[req.state] ?? "Posted update",
  );

export const useUpdateIncident = (id: string) =>
  useAction(
    (req: Schema["UpdateIncidentRequest"]) =>
      body(
        api.PATCH("/api/v1/incidents/{incident}", {
          params: { path: { incident: id } },
          body: req,
        }),
      ).then(toIncident),
    () => [statusKeys.all],
  );

export const useEditIncidentUpdate = (id: string) =>
  useAction(
    ({ update, message }: { update: number; message: string }) =>
      body(
        api.PATCH("/api/v1/incidents/{incident}/updates/{update}", {
          params: { path: { incident: id, update } },
          body: { message },
        }),
      ).then(toIncident),
    () => [statusKeys.all],
    () => "Edited update",
  );

export const useDeleteIncident = (id: string) =>
  useAction(
    () =>
      done(
        api.DELETE("/api/v1/incidents/{incident}", {
          params: { path: { incident: id } },
        }),
      ),
    () => [statusKeys.all],
    () => `Removed incident ${id}`,
  );

/** How an incident's state reads in the editor. */
export const incidentState: Record<IncidentState, State> = {
  draft: { tone: "mut", glyph: "○", label: "draft" },
  investigating: { tone: "warn", glyph: "▲", label: "investigating" },
  identified: { tone: "warn", glyph: "▲", label: "identified" },
  monitoring: { tone: "info", glyph: "◐", label: "monitoring" },
  resolved: { tone: "ok", glyph: "●", label: "resolved" },
  scheduled: { tone: "info", glyph: "○", label: "scheduled" },
  in_progress: { tone: "info", glyph: "◐", label: "in progress" },
  completed: { tone: "ok", glyph: "●", label: "completed" },
  cancelled: { tone: "mut", glyph: "○", label: "cancelled" },
};

/** The impacts an incident may have on a component, mildest first. */
export const impacts = [
  "degraded",
  "partial_outage",
  "major_outage",
] as const satisfies StatusImpact["impact"][];

export type StatusHook = Schema["StatusHook"];

export const useAddStatusHook = (slug: string) =>
  useAction(
    (req: Schema["AddStatusHookRequest"]) =>
      body(
        api.POST("/api/v1/status-pages/{page}/hooks", {
          params: { path: { page: slug } },
          body: req,
        }),
      ),
    () => [statusKeys.hooks(slug)],
    (req) => `Added hook ${req.name}`,
  );

export const useRemoveStatusHook = (slug: string) =>
  useAction(
    (name: string) =>
      done(
        api.DELETE("/api/v1/status-pages/{page}/hooks/{hook}", {
          params: { path: { page: slug, hook: name } },
        }),
      ),
    () => [statusKeys.hooks(slug)],
    (name) => `Removed hook ${name}`,
  );

export const useTestStatusHook = (slug: string) =>
  useAction(
    (name: string) =>
      body(
        api.POST("/api/v1/status-pages/{page}/hooks/{hook}/test", {
          params: { path: { page: slug, hook: name } },
        }),
      ),
    () => [statusKeys.hooks(slug)],
    (name, res) =>
      res.ok
        ? `Posted a test update to ${name}`
        : `${name} failed: ${res.error}`,
  );
