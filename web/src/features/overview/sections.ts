/**
 * The sections of a project's Settings tab, in order: the list on the
 * page and the palette's search both read this. `words` are what people
 * type looking for it. Kept free of React so the palette can load it
 * cheaply.
 */
export const projectSettingSections = [
  { id: "general", label: "General", words: ["name", "rename", "address"] },
  {
    id: "variables",
    label: "Variables",
    words: ["shared variables", "env", "environment variables"],
  },
  {
    id: "previews",
    label: "Previews",
    words: ["pull requests", "preview environments", "branches"],
  },
  {
    id: "placement",
    label: "Placement",
    words: ["server", "move", "which server"],
  },
  {
    id: "log-drains",
    label: "Log drains",
    words: ["logs", "syslog", "forward logs"],
  },
  {
    id: "config",
    label: "Config file",
    words: ["export", "apply", "toml", "template"],
  },
  {
    id: "archive",
    label: "Archive",
    words: ["archive", "delete project"],
    danger: true,
  },
] as const;
