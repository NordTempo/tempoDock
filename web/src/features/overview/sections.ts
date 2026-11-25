/**
 * The settings of a project, in page order: the palette's search reads
 * this. `words` are what people type looking for it. `scope` says which
 * page holds it (ADR 0099): "environment", the Settings tab, which shows
 * the environment picked in the header and nothing else; "project",
 * Project settings, which apply to every environment. Kept free of React
 * so the palette can load it cheaply.
 */
export const projectSettingSections = [
  {
    id: "variables",
    label: "Shared variables",
    scope: "environment",
    words: ["shared variables", "env", "environment variables"],
  },
  {
    id: "server",
    label: "Server",
    scope: "environment",
    words: ["server", "move", "which server", "placement"],
  },
  {
    id: "log-drains",
    label: "Log drains",
    scope: "environment",
    words: ["logs", "syslog", "forward logs"],
  },
  {
    id: "general",
    label: "Name",
    scope: "project",
    words: ["name", "rename", "address"],
  },
  {
    id: "previews",
    label: "Previews",
    scope: "project",
    words: ["pull requests", "preview environments", "branches"],
  },
  {
    id: "placement",
    label: "New environments",
    scope: "project",
    words: ["placement", "kind", "which server"],
  },
  {
    id: "config",
    label: "Config file",
    scope: "project",
    words: ["export", "apply", "toml", "template"],
  },
  {
    id: "archive",
    label: "Archive",
    scope: "project",
    words: ["archive", "delete project"],
  },
] as const;

/** The #hashes that Project settings holds, for old Settings links. */
export const projectScoped: ReadonlySet<string> = new Set(
  projectSettingSections.filter((s) => s.scope === "project").map((s) => s.id),
);
