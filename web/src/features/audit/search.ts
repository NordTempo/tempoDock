// The audit log's search, apart from its page so the routes that show it
// can validate their URLs without loading the page with the first route.

/** The most events the API returns at once. */
export const MAX = 500;

/** The audit log's filters, kept in the URL by the route that shows it. */
export type AuditSearch = {
  /** The platform's log instead of the organization's (operators). */
  platform?: true;
  project?: string;
  /** Needs project. */
  environment?: string;
  /** Action prefix, filtered by the API. */
  action?: string;
  /** Up to MAX events instead of the API's page of 100 (`dock audit -n 500`). */
  limit?: typeof MAX;
  /** The rest filter the loaded page of events. */
  kind?: "token" | "user";
  actor?: string;
  q?: string;
};

const str = (v: unknown) => (typeof v === "string" && v ? v : undefined);

/** Reads AuditSearch from a route's search params. */
export const validateAuditSearch = (
  s: Record<string, unknown>,
): AuditSearch => ({
  platform: s.platform === true || s.platform === "true" ? true : undefined,
  project: str(s.project),
  environment: str(s.project) && str(s.environment),
  action: str(s.action),
  limit: Number(s.limit) === MAX ? MAX : undefined,
  kind: s.kind === "token" || s.kind === "user" ? s.kind : undefined,
  actor: str(s.actor),
  q: str(s.q),
});
