/**
 * The screens a help topic's "Do" rows link to, by scope: a service's
 * tabs need a service in view, a project's pages a project, the
 * organization's pages only the org. Topics name them as `{ s: "logs" }`,
 * `{ p: "compare" }` or `{ o: "inbox" }`. A `hash` is a section of a
 * Settings page (features/settings/sections.ts, overview/sections.ts).
 */
export const servicePlaces = {
  overview: { to: "/$org/$project/$environment/$service", label: "Overview" },
  logs: { to: "/$org/$project/$environment/$service/logs", label: "Logs" },
  requests: {
    to: "/$org/$project/$environment/$service/requests",
    label: "Requests",
  },
  traces: {
    to: "/$org/$project/$environment/$service/requests/traces",
    label: "Traces",
  },
  visitors: {
    to: "/$org/$project/$environment/$service/requests/visitors",
    label: "Visitors",
  },
  errors: {
    to: "/$org/$project/$environment/$service/errors",
    label: "Errors",
  },
  metrics: {
    to: "/$org/$project/$environment/$service/metrics",
    label: "Metrics",
  },
  jobs: { to: "/$org/$project/$environment/$service/jobs", label: "Jobs" },
  shell: { to: "/$org/$project/$environment/$service/shell", label: "Shell" },
  data: { to: "/$org/$project/$environment/$service/data", label: "Data" },
  variables: {
    to: "/$org/$project/$environment/$service/variables",
    label: "Variables",
  },
  settings: {
    to: "/$org/$project/$environment/$service/settings",
    label: "Settings",
  },
  source: {
    to: "/$org/$project/$environment/$service/settings",
    hash: "source",
    label: "Settings › Source",
  },
  health: {
    to: "/$org/$project/$environment/$service/settings",
    hash: "health",
    label: "Health and uptime",
  },
  domains: {
    to: "/$org/$project/$environment/$service/settings",
    hash: "domains",
    label: "Domains",
  },
  backups: {
    to: "/$org/$project/$environment/$service/settings",
    hash: "backups",
    label: "Backups",
  },
  history: {
    to: "/$org/$project/$environment/$service/history",
    label: "History",
  },
} as const;

export const projectPlaces = {
  compare: { to: "/$org/$project/compare", label: "Compare" },
  drains: {
    to: "/$org/$project/settings",
    hash: "log-drains",
    label: "Log drains",
  },
  compose: {
    to: "/$org/$project/settings",
    hash: "compose",
    label: "Settings › Compose",
  },
  shared: {
    to: "/$org/$project/settings",
    hash: "variables",
    label: "Shared variables",
  },
} as const;

export const orgPlaces = {
  overview: { to: "/$org", label: "Projects" },
  errors: { to: "/$org/errors", label: "Errors" },
  analytics: { to: "/$org/analytics", label: "Analytics" },
  images: { to: "/$org/images", label: "Images" },
  statusPages: { to: "/$org/status-pages", label: "Status pages" },
  inbox: { to: "/$org/inbox", label: "Inbox" },
  alerts: { to: "/$org/settings/alerts", label: "Alerts settings" },
  members: { to: "/$org/settings/members", label: "Members" },
  groups: { to: "/$org/settings/groups", label: "Groups" },
  tokens: { to: "/$org/settings/tokens", label: "Tokens" },
  variables: { to: "/$org/settings/variables", label: "Variables" },
  github: { to: "/$org/settings/github", label: "GitHub" },
  cloudflare: { to: "/$org/settings/cloudflare", label: "Cloudflare" },
  registries: { to: "/$org/settings/registries", label: "Registries" },
  certificates: { to: "/$org/settings/certificates", label: "Certificates" },
  activity: { to: "/$org/settings/activity", label: "Activity" },
  archived: { to: "/$org/settings/archived", label: "Archived projects" },
} as const;

/** Where a "Do" row's link goes. */
export type Place =
  | { s: keyof typeof servicePlaces }
  | { p: keyof typeof projectPlaces }
  | { o: keyof typeof orgPlaces }
  | { account: true }
  | { console: true };
