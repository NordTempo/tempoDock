import type { IconName } from "../../ui/icons";

/**
 * The platform console's navigation, grouped by job (ADR 0099): ten
 * entries in the sidebar instead of eighteen. An entry with several
 * pages opens on the first; the others are a tab row under the page's
 * title (ConsoleTabs), and the sidebar marks the entry on any of them.
 * Every page keeps its own address. The palette reads this too.
 */
export type ConsolePage = {
  to:
    | "/console"
    | "/console/servers"
    | "/console/orgs"
    | "/console/features"
    | "/console/backups"
    | "/console/storage"
    | "/console/registry"
    | "/console/users"
    | "/console/tokens"
    | "/console/sign-in"
    | "/console/data-access"
    | "/console/github"
    | "/console/cloudflare"
    | "/console/email"
    | "/console/analytics"
    | "/console/address"
    | "/console/alerts"
    | "/console/audit"
    | "/console/updates";
  label: string;
};

/** One sidebar entry: its name, icon and pages (the first is where it opens). */
export type ConsoleEntry = {
  label: string;
  icon: IconName;
  pages: [ConsolePage, ...ConsolePage[]];
};

export const consoleNav: ConsoleEntry[] = [
  {
    label: "Overview",
    icon: "grid",
    pages: [{ to: "/console", label: "Overview" }],
  },
  {
    label: "Servers",
    icon: "server",
    pages: [{ to: "/console/servers", label: "Servers" }],
  },
  {
    label: "Organizations",
    icon: "users",
    pages: [{ to: "/console/orgs", label: "Organizations" }],
  },
  {
    label: "Features",
    icon: "sliders",
    pages: [{ to: "/console/features", label: "Features" }],
  },
  {
    label: "Backups and storage",
    icon: "history",
    pages: [
      { to: "/console/backups", label: "Backups" },
      { to: "/console/storage", label: "Storage buckets" },
      { to: "/console/registry", label: "Registry" },
    ],
  },
  {
    label: "People and access",
    icon: "lock",
    pages: [
      { to: "/console/users", label: "Users" },
      { to: "/console/tokens", label: "Tokens" },
      { to: "/console/sign-in", label: "Sign-in" },
      { to: "/console/data-access", label: "Data access" },
    ],
  },
  {
    label: "Connections",
    icon: "cloud",
    pages: [
      { to: "/console/github", label: "GitHub" },
      { to: "/console/cloudflare", label: "Cloudflare" },
      { to: "/console/email", label: "Email" },
      { to: "/console/analytics", label: "Analytics" },
      { to: "/console/address", label: "Dock's address" },
    ],
  },
  {
    label: "Alerts",
    icon: "bell",
    pages: [{ to: "/console/alerts", label: "Alerts" }],
  },
  {
    label: "Activity",
    icon: "activity",
    pages: [{ to: "/console/audit", label: "Activity" }],
  },
  {
    label: "Updates",
    icon: "upload",
    pages: [{ to: "/console/updates", label: "Updates" }],
  },
];

/** The entry a console address belongs to (its pages, or a server's page under Servers). */
export const consoleEntryOf = (pathname: string) =>
  consoleNav.find((e) =>
    e.pages.some((p) =>
      p.to === "/console"
        ? pathname === "/console" || pathname === "/console/"
        : pathname === p.to || pathname.startsWith(`${p.to}/`),
    ),
  );
