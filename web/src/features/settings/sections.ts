/**
 * The sections of a service's Settings page, in order, for the palette's
 * search: each id is a section's anchor on the page (settings#domains).
 * `words` are what people type looking for it. Kept free of React so the
 * palette can load it cheaply.
 */
export const serviceSettingSections = [
  {
    id: "source",
    label: "Source",
    words: ["image", "repository", "github", "build", "dockerfile", "previews"],
  },
  {
    id: "running",
    label: "Running",
    words: [
      "port",
      "start command",
      "limits",
      "cpu",
      "memory",
      "volumes",
      "server",
    ],
  },
  {
    id: "deploys",
    label: "Deploys",
    words: ["downtime", "before it goes live", "migrations", "release command"],
  },
  {
    id: "health",
    label: "Health and uptime",
    words: ["health check", "uptime", "checks while running", "restarts"],
  },
  {
    id: "domains",
    label: "Domains",
    words: ["domain", "hostname", "dns", "certificate", "visits", "access"],
  },
  {
    id: "backups",
    label: "Backups",
    words: ["backup", "restore", "snapshot", "point in time"],
    stateful: true,
  },
  {
    id: "archive",
    label: "Archive",
    words: ["archive", "delete", "remove service"],
  },
] as const;
