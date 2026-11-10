import { queryOptions } from "@tanstack/react-query";
import { api, body, done, list } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import type { State } from "../../ui/status";

// The organization's public status pages (ADR 0073). What a page says is
// worked out on the server from uptime checks, alerts and sleep, so
// data/live.ts refetches these on uptime, alert and sleep events and on
// status_page.* audit events.

type Schema = components["schemas"];
type PageSchema = Schema["StatusPage"];
type ComponentSchema = Schema["StatusComponent"];

/** A component, with the lists the API may send as null as arrays. */
export type StatusComponent = Omit<ComponentSchema, "days" | "sources"> & {
  days: NonNullable<ComponentSchema["days"]>;
  sources: NonNullable<ComponentSchema["sources"]>;
};
/** A page with its components. */
export type StatusPage = Omit<PageSchema, "components"> & {
  components: StatusComponent[];
};
export type StatusComponentInput = Schema["StatusComponentInput"];
export type StatusSourceRef = Schema["StatusSourceRef"];
export type ComponentStatus = PageSchema["status"];
type PagePatch = Schema["UpdateStatusPageRequest"];

const statusKeys = {
  all: ["status-pages"],
  list: ["status-pages", "list"],
  page: (slug: string) => ["status-pages", "page", slug],
  sources: ["status-pages", "sources"],
};

/** The API's page, its lists as arrays. */
const toPage = (p: PageSchema): StatusPage => ({
  ...p,
  components: (p.components ?? []).map((c) => ({
    ...c,
    days: c.days ?? [],
    sources: c.sources ?? [],
  })),
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
  degraded: { tone: "warn", glyph: "▲", label: "degraded" },
  partial_outage: { tone: "warn", glyph: "▲", label: "partial outage" },
  major_outage: { tone: "fail", glyph: "✕", label: "major outage" },
};
