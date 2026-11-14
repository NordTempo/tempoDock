/**
 * The screens a help topic's "Do" rows link to, by scope: a service's
 * tabs need a service in view, a project's pages a project, the
 * organization's pages only the org. Topics name them as `{ s: "logs" }`,
 * `{ p: "previews" }` or `{ o: "inbox" }`.
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
  data: { to: "/$org/$project/$environment/$service/data", label: "Data" },
  variables: {
    to: "/$org/$project/$environment/$service/variables",
    label: "Variables",
  },
  settings: {
    to: "/$org/$project/$environment/$service/settings",
    label: "Settings",
  },
  domains: {
    to: "/$org/$project/$environment/$service/settings/domains",
    label: "Domains",
  },
  backups: {
    to: "/$org/$project/$environment/$service/settings/backups",
    label: "Backups",
  },
  history: {
    to: "/$org/$project/$environment/$service/history",
    label: "History",
  },
} as const;

export const projectPlaces = {
  compare: { to: "/$org/$project/compare", label: "Compare" },
  previews: { to: "/$org/$project/settings/previews", label: "Previews" },
  drains: { to: "/$org/$project/settings/log-drains", label: "Log drains" },
  shared: {
    to: "/$org/$project/settings/variables",
    label: "Shared variables",
  },
} as const;

export const orgPlaces = {
  overview: { to: "/$org", label: "Overview" },
  errors: { to: "/$org/errors", label: "Errors" },
  analytics: { to: "/$org/analytics", label: "Analytics" },
  images: { to: "/$org/images", label: "Images" },
  statusPages: { to: "/$org/status-pages", label: "Status pages" },
  inbox: { to: "/$org/inbox", label: "Inbox" },
  alerts: { to: "/$org/settings/alerts", label: "Alert channels" },
  members: { to: "/$org/settings/members", label: "Members" },
  groups: { to: "/$org/settings/groups", label: "Groups" },
  tokens: { to: "/$org/settings/tokens", label: "Tokens" },
  variables: { to: "/$org/settings/variables", label: "Variables" },
  github: { to: "/$org/settings/github", label: "GitHub" },
  cloudflare: { to: "/$org/settings/cloudflare", label: "Cloudflare" },
  registries: { to: "/$org/settings/registries", label: "Registries" },
  activity: { to: "/$org/settings/activity", label: "Activity" },
} as const;

/** Where a "Do" row's link goes. */
export type Place =
  | { s: keyof typeof servicePlaces }
  | { p: keyof typeof projectPlaces }
  | { o: keyof typeof orgPlaces }
  | { account: true }
  | { console: true };
