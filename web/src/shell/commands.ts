import { useQueries } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useLogout } from "../data/mutations";
import {
  canDeploy,
  deployBlock,
  queries,
  serviceRef,
  useProjectTreeIf,
  useSeesPlatform,
  useServersIfOperator,
  useSession,
  whyNot,
} from "../data/queries";
import { type ConsolePage, consoleNav } from "../features/console/nav";
import {
  canCreateEnvironment,
  canCreateProject,
} from "../features/create/access";
import { openCreate } from "../features/create/open";
import { openDeploy } from "../features/deploy/start";
import { openHelp } from "../features/help/open";
import { requestLock } from "../features/overview/lockRequest";
import { projectSettingSections } from "../features/overview/sections";
import { promoteCli, promotionPairs } from "../features/promote/plan";
import { openQueue } from "../features/queue/open";
import { rollbackTarget } from "../features/service/releases";
import { tabsFor } from "../features/service/tabs";
import { serviceSettingSections } from "../features/settings/sections";
import { serviceState, type Tone } from "../ui/status";
import { openConfirm } from "./confirmHost";
import { useCurrentContext, useOrg } from "./location";
import { useShellMode } from "./Sidebar";

export type Command = {
  id: string;
  /**
   * Actions change something; Settings are the sections of a Settings
   * page, found by what they hold ("domain", "backup"); Go to opens a page.
   */
  group: "Actions" | "Settings" | "Go to";
  /** Plain text, matched against the query. */
  label: string;
  /** A part of the label shown bold, usually the service name. */
  strong?: string;
  /** A short note after the label, like "↻ crash looping now". */
  note?: { text: string; tone: Tone };
  /** The equivalent `dock` CLI invocation, or the page's key sequence. */
  cli: string;
  /** More words it is found by, not shown. */
  keywords?: string;
  /** Context relevance: the current service beats the current project. */
  weight: number;
  run: () => unknown;
};

/**
 * Every palette command, built from the project tree and current location:
 * actions, every section of the Settings pages in reach (the current
 * project's services and every project, the organization's, the console's
 * for operators) and pages to go to. Rollback commands need release
 * history, which is fetched only while `withHistory` (the palette open);
 * until then only histories already cached count.
 */
export function useCommands({
  withHistory,
}: {
  withHistory: boolean;
}): Command[] {
  // The console has no organization, so no projects (ADR 0062).
  const projects = useProjectTreeIf(useShellMode() !== "console");
  const here = useCurrentContext();
  const org = useOrg();
  const navigate = useNavigate();
  const logout = useLogout();
  const operator = useSeesPlatform();
  const session = useSession();
  const servers = useServersIfOperator();

  // Rollback targets need release history, for the current project.
  const local = projects
    .filter((p) => p.project.name === here.project)
    .flatMap((p) => p.environments.flatMap((e) => e.services))
    .map((s) => serviceRef(s.service));
  const histories = useQueries({
    queries: local.map((ref) => ({
      ...queries.releases(ref),
      staleTime: 60_000,
      enabled: withHistory,
    })),
  });
  const historyOf = (project: string, environment: string, service: string) =>
    histories[
      local.findIndex(
        (r) =>
          r.project === project &&
          r.environment === environment &&
          r.service === service,
      )
    ]?.data;

  const commands: Command[] = [];
  const add = (c: Command) => commands.push(c);

  for (const { project: p, environments } of projects) {
    const project = p.name;
    const inProject = project === here.project;
    const pFlag = inProject ? "" : ` -p ${project}`;
    add({
      id: `go:${project}`,
      group: "Go to",
      label: `${project} overview`,
      strong: project,
      cli: `dock status${pFlag}`,
      weight: inProject ? 1 : 0,
      run: () => navigate({ to: "/$org/$project", params: { org, project } }),
    });
    if (canCreateEnvironment(session, project)) {
      add({
        id: `new-env:${project}`,
        group: "Actions",
        label: `New environment in ${project}`,
        strong: project,
        cli: `dock environment create${pFlag}`,
        weight: inProject ? 0 : -1,
        run: () => openCreate({ kind: "environment", project }),
      });
    }
    add({
      id: `settings:${project}`,
      group: "Go to",
      label: `${project} settings`,
      strong: project,
      cli: inProject ? "S" : "",
      weight: inProject ? 1 : 0,
      run: () =>
        navigate({ to: "/$org/$project/settings", params: { org, project } }),
    });
    // Each setting of the project, found by what it holds, on its page:
    // the Settings tab for an environment's, Project settings for the rest.
    for (const section of projectSettingSections) {
      add({
        id: `setting:${project}:${section.id}`,
        group: "Settings",
        label: `${section.label} · ${project}`,
        strong: project,
        keywords: `settings ${section.words.join(" ")}`,
        cli: "",
        weight: inProject ? 1 : -0.5,
        run: () =>
          navigate({
            to:
              section.scope === "project"
                ? "/$org/$project/project-settings"
                : "/$org/$project/settings",
            params: { org, project },
            hash: section.id,
          }),
      });
    }
    if (environments.length > 1) {
      add({
        id: `compare:${project}`,
        group: "Go to",
        label: `Compare ${project} environments`,
        strong: project,
        cli: inProject ? "C" : "",
        weight: inProject ? 1 : 0,
        run: () =>
          navigate({ to: "/$org/$project/compare", params: { org, project } }),
      });
    }
    // Promote opens the compare page's promote panel, which previews it.
    for (const { from, to } of promotionPairs(environments)) {
      const target = { project, environment: to.environment.name };
      if (!canDeploy(session, target)) continue;
      const blocked = deployBlock(to.environment);
      add({
        id: `promote:${project}/${from.environment.name}/${to.environment.name}`,
        group: "Actions",
        label: `Promote ${from.environment.name} → ${to.environment.name}${inProject ? "" : ` · ${project}`}`,
        strong: to.environment.name,
        note: blocked ? { text: blocked, tone: "fail" } : undefined,
        cli: `${promoteCli(from.environment.name, to.environment.name)}${pFlag}`,
        weight: inProject ? 1.5 : 0,
        run: () =>
          navigate({
            to: "/$org/$project/compare",
            params: { org, project },
            search: {
              from: from.environment.name,
              to: to.environment.name,
              promote: true,
            },
          }),
      });
    }

    for (const { environment: env, services } of environments) {
      const environment = env.name;
      const inEnv = inProject && environment === here.environment;
      // Releases can't start on an unreachable server.
      const blocked = deployBlock(env);
      const refuse = blocked
        ? () => {
            throw new Error(`Can't deploy: ${blocked}.`);
          }
        : undefined;
      const blockedNote = blocked
        ? { text: blocked, tone: "fail" as const }
        : undefined;
      if (canDeploy(session, { project, environment })) {
        const ref = { project, environment };
        const at = inProject ? environment : `${project} ${environment}`;
        const weight = (inProject ? 0 : -1) + (inEnv ? 0.5 : 0);
        add({
          id: `new-service:${project}/${environment}`,
          group: "Actions",
          label: `New service in ${at}`,
          strong: environment,
          cli: `dock service create -e ${environment}${pFlag}`,
          weight,
          run: () => openCreate({ kind: "service", env: ref }),
        });
        add({
          id: `new-db:${project}/${environment}`,
          group: "Actions",
          label: `New database in ${at}`,
          strong: environment,
          cli: `dock service create --template postgres -e ${environment}${pFlag}`,
          weight: weight - 0.1,
          run: () =>
            openCreate({
              kind: "service",
              env: ref,
              source: "template",
              template: "postgres",
            }),
        });
      }
      // Lock: the environment's lock form on the project page (admins).
      if (session?.scope === "admin" && !env.lock) {
        add({
          id: `lock:${project}/${environment}`,
          group: "Actions",
          label: `Lock ${environment}${inProject ? "" : ` · ${project}`}`,
          strong: environment,
          cli: `dock environment lock ${environment}${pFlag}`,
          weight: (inProject ? 0.5 : -1) + (inEnv ? 0.5 : 0),
          run: async () => {
            await navigate({
              to: "/$org/$project/$environment",
              params: { org, project, environment },
            });
            requestLock({ project, environment });
          },
        });
      }
      const mayDeploy = canDeploy(session, { project, environment });
      add({
        id: `vars:${project}/${environment}`,
        group: "Go to",
        label: `Variables · ${project} ${environment}`,
        strong: environment,
        cli: `dock env list -e ${environment}${pFlag}`,
        weight: (inProject ? 1 : 0) + (inEnv ? 1 : 0),
        run: () =>
          navigate({
            to: "/$org/$project/settings",
            params: { org, project },
            search: { env: environment },
            hash: "variables",
          }),
      });

      for (const svc of services) {
        const params = serviceRef(svc.service);
        const { service } = params;
        const id = `${project}/${environment}/${service}`;
        const flags = ` -s ${service} -e ${environment}${pFlag}`;
        const isHere = inEnv && service === here.service;
        const weight = isHere ? 4 : (inProject ? 2 : 0) + (inEnv ? 1 : 0);
        // Outside the current project, labels name the project too.
        const where = inProject ? environment : `${project} ${environment}`;
        const state = serviceState(svc);
        const note =
          state.tone === "fail" || state.tone === "prog"
            ? {
                text: `${state.glyph ?? ""} ${state.label} now`.trim(),
                tone: state.tone,
              }
            : undefined;
        const open = () =>
          navigate({
            to: "/$org/$project/$environment/$service",
            params: { org, ...params },
          });
        const busy =
          blockedNote ??
          (svc.deploying
            ? {
                text: `r${svc.deploying.number} is deploying`,
                tone: "prog" as const,
              }
            : undefined);

        // The Deploy window takes a folder, an image or the GitHub branch.
        if (mayDeploy) {
          add({
            id: `deploy:${id}`,
            group: "Actions",
            label: `Deploy ${service} ${where}`,
            strong: service,
            note: busy ?? note,
            cli: `dock deploy${flags}`,
            weight,
            run: refuse ?? (() => openDeploy(params)),
          });
        }
        if (mayDeploy && svc.service.active_release) {
          add({
            id: `restart:${id}`,
            group: "Actions",
            label: `Restart ${service} ${where}`,
            strong: service,
            note: busy ?? note,
            cli: `dock restart${flags}`,
            weight: weight - 0.5,
            // The same confirm as the service's Restart button.
            run:
              refuse ?? (() => openConfirm({ kind: "restart", ref: params })),
          });
        }
        const target = rollbackTarget(
          historyOf(project, environment, service) ?? [],
        );
        if (mayDeploy && target) {
          add({
            id: `rollback:${id}`,
            group: "Actions",
            label: `Roll back ${service} ${where} to r${target.number}`,
            strong: service,
            note: busy ?? note,
            cli: `dock rollback ${target.number}${flags}`,
            weight: weight - 0.5,
            // The same confirm as the releases' Roll back.
            run:
              refuse ??
              (() =>
                openConfirm({
                  kind: "rollback",
                  ref: params,
                  to: target.number,
                })),
          });
        }
        add({
          id: `go:${id}`,
          group: "Go to",
          label: `${service} ${where}`,
          strong: service,
          note,
          cli: `dock status${flags}`,
          weight,
          run: open,
        });
        for (const tab of tabsFor(
          svc,
          whyNot(session, "admin", params) === undefined,
        )) {
          if (tab.label === "Overview") continue;
          add({
            id: `go:${id}:${tab.label}`,
            group: "Go to",
            label: `${tab.label} · ${service} ${where}`,
            strong: service,
            cli: isHere ? tab.key.toUpperCase() : "",
            weight: weight - 1,
            run: () => navigate({ to: tab.to, params: { org, ...params } }),
          });
        }
        // The current project's services only: every section of every
        // service in the organization would drown the rest.
        if (!inProject) continue;
        const settings = (hash: string) => () =>
          navigate({
            to: "/$org/$project/$environment/$service/settings",
            params: { org, ...params },
            hash,
          });
        for (const section of serviceSettingSections) {
          if ("stateful" in section && !svc.service.stateful) continue;
          add({
            id: `setting:${id}:${section.id}`,
            group: "Settings",
            label: `${section.label} · ${service} ${where}`,
            strong: service,
            keywords: `settings ${section.words.join(" ")}`,
            cli: "",
            weight: weight - 1,
            run: settings(section.id),
          });
        }
        // What people look for by the task's name: both open the place
        // where it's done (the Domains section, the Jobs tab's command).
        if (mayDeploy) {
          add({
            id: `add-domain:${id}`,
            group: "Actions",
            label: `Add a domain to ${service} ${where}`,
            strong: service,
            keywords: "hostname dns",
            cli: `dock domain add <hostname>${flags}`,
            weight: weight - 1,
            run: settings("domains"),
          });
          add({
            id: `run:${id}`,
            group: "Actions",
            label: `Run a command in ${service} ${where}`,
            strong: service,
            keywords: "exec shell one-off migrate",
            cli: `dock run ${service} -e ${environment}${pFlag} -- <command>`,
            weight: weight - 1,
            run: () =>
              navigate({
                to: "/$org/$project/$environment/$service/jobs",
                params: { org, ...params },
              }),
          });
        }
      }
    }
  }

  if (canCreateProject(session)) {
    add({
      id: "new-project",
      group: "Actions",
      label: "New project",
      cli: "dock project create",
      weight: -0.5,
      run: () => openCreate({ kind: "project" }),
    });
  }

  // Operators reach the platform console's pages (its sidebar's entries
  // and the pages under them); everyone their account.
  if (operator) {
    for (const entry of consoleNav) {
      for (const page of entry.pages) {
        add({
          id: `go:${page.to}`,
          group: "Go to",
          label:
            page.to === "/console"
              ? "Platform console"
              : entry.pages.length > 1
                ? `${page.label} · ${entry.label}`
                : page.label,
          keywords: `console platform ${entry.label}`,
          cli: consoleCli[page.to] ?? "",
          weight: 0,
          run: () => navigate({ to: page.to }),
        });
      }
    }
  }
  add({
    id: "go:/account",
    group: "Go to",
    label: "Account",
    cli: "dock whoami",
    weight: 0,
    run: () => navigate({ to: "/account" }),
  });
  const orgPages = [
    ["Projects", "/$org", "dock project list"],
    ["Inbox", "/$org/inbox", "dock alerts"],
    ["Errors", "/$org/errors", "dock errors"],
    ["Analytics", "/$org/analytics", "dock analytics sites"],
    ["Images", "/$org/images", "dock images"],
    ["Vulnerability ignores", "/$org/images/ignores", "dock vulns ignores"],
    ["Status", "/$org/status", "dock status-page list"],
  ] as const;
  for (const [label, to, cli] of orgPages) {
    add({
      id: `go:${to}`,
      group: "Go to",
      label,
      cli,
      weight: 0,
      run: () => navigate({ to, params: { org } }),
    });
  }
  // The organization's Settings sections, by what they hold; the ones
  // only admins can use for admins.
  const admin = session?.scope === "admin";
  for (const s of orgSettingSections) {
    if (s.admin && !admin) continue;
    // Operators read the platform's activity on the console.
    if (s.to === "/$org/settings/activity" && operator) continue;
    add({
      id: `go:${s.to}`,
      group: "Settings",
      label: `${s.label} · organization`,
      keywords: `settings ${s.words}`,
      cli: s.cli,
      weight: 0,
      run: () => navigate({ to: s.to, params: { org } }),
    });
  }
  // Server pages (statistics, placement) are in the console.
  for (const { name } of servers) {
    add({
      id: `go:server:${name}`,
      group: "Go to",
      label: `Server ${name}`,
      strong: name,
      cli: `dock server show ${name}`,
      weight: 0,
      run: () =>
        navigate({ to: "/console/servers/$server", params: { server: name } }),
    });
  }
  add({
    id: "help",
    group: "Go to",
    label: "Help for this page",
    keywords: "keyboard shortcuts keys",
    cli: "?",
    weight: -1,
    run: () => openHelp(),
  });
  add({
    id: "deploys",
    group: "Go to",
    label: "Deploy queue",
    keywords: "builds builder waiting queued running cancel",
    cli: "dock queue",
    weight: 0,
    run: () => openQueue(),
  });
  add({
    id: "logout",
    group: "Actions",
    label: "Log out",
    cli: "dock logout",
    weight: -1,
    run: () => logout.mutateAsync(),
  });

  return commands;
}

export type Match = { command: Command; score: number };

/**
 * Every query word must appear in the label or the CLI text. Matches at
 * word starts and context weight rank higher. An empty query lists the
 * most relevant items.
 */
export function matchCommands(commands: Command[], query: string): Match[] {
  const words = query
    .toLowerCase()
    .replace(/^dock\s+/, "")
    .split(/\s+/)
    .filter(Boolean);
  const scored: Match[] = [];
  for (const command of commands) {
    const text =
      `${command.label} ${command.cli} ${command.keywords ?? ""}`.toLowerCase();
    let score = command.weight;
    let all = true;
    for (const word of words) {
      const at = text.indexOf(word);
      if (at < 0) {
        all = false;
        break;
      }
      if (at === 0 || /[\s/·-]/.test(text[at - 1] ?? "")) score += 3;
      score -= at / 100;
    }
    if (all) scored.push({ command, score });
  }
  scored.sort(
    (a, b) =>
      groupOrder[a.command.group] - groupOrder[b.command.group] ||
      b.score - a.score,
  );
  const limit = words.length ? 6 : 4;
  const of = (group: Command["group"]) =>
    scored.filter((m) => m.command.group === group).slice(0, limit);
  // Settings sections come up once something is typed: listed untyped,
  // they would push out the places and actions people come for.
  return [
    ...of("Actions"),
    ...(words.length ? of("Settings") : []),
    ...of("Go to"),
  ];
}

/** The `dock` command of each console page, for the palette's hint. */
const consoleCli: Partial<Record<ConsolePage["to"], string>> = {
  "/console/servers": "dock server list",
  "/console/registry": "dock registry",
  "/console/storage": "dock storage",
  "/console/backups": "dock backup status",
  "/console/users": "dock user list",
  "/console/sign-in": "dock sso list",
  "/console/email": "dock email",
  "/console/data-access": "dock db policy show",
  "/console/alerts": "dock alerts --platform",
  "/console/audit": "dock audit",
  "/console/github": "dock github",
  "/console/cloudflare": "dock cloudflare",
  "/console/address": "dock address",
  "/console/updates": "dock update",
  "/console/analytics": "dock analytics",
};

const groupOrder: Record<Command["group"], number> = {
  Actions: 0,
  Settings: 1,
  "Go to": 2,
};

/**
 * The organization's Settings sections for search: where each is, what it
 * holds (`words`), its CLI and whether only admins use it. The same
 * sections as the Settings column (settingsGroups in
 * features/org/SettingsBar.tsx), kept here as plain data so the shell's
 * initial bundle doesn't take that page's components and queries.
 */
const orgSettingSections = [
  {
    to: "/$org/settings/general",
    label: "General",
    admin: false,
    cli: "dock org list",
    words: "name address two-factor sign-in lock new production",
  },
  {
    to: "/$org/settings/variables",
    label: "Variables",
    admin: false,
    cli: "dock org env list",
    words: "organization variables shared secrets",
  },
  {
    to: "/$org/settings/activity",
    label: "Activity",
    admin: false,
    cli: "dock audit",
    words: "audit log history who changed",
  },
  {
    to: "/$org/settings/members",
    label: "Members",
    admin: true,
    cli: "dock org members",
    words: "people invite users remove",
  },
  {
    to: "/$org/settings/groups",
    label: "Groups",
    admin: true,
    cli: "dock group list",
    words: "teams people",
  },
  {
    to: "/$org/settings/roles",
    label: "Roles",
    admin: true,
    cli: "dock role list",
    words: "permissions access",
  },
  {
    to: "/$org/settings/agents",
    label: "Agent rules",
    admin: true,
    cli: "dock agent rules",
    words: "agents mcp ai",
  },
  {
    to: "/$org/settings/tokens",
    label: "Tokens",
    admin: true,
    cli: "dock token list",
    words: "api keys deploy token ci",
  },
  {
    to: "/$org/settings/alerts",
    label: "Alerts",
    admin: false,
    cli: "dock alerts channel list",
    words: "notifications slack webhook email channels vulnerabilities",
  },
  {
    to: "/$org/settings/github",
    label: "GitHub",
    admin: true,
    cli: "dock github",
    words: "repository git app install",
  },
  {
    to: "/$org/settings/cloudflare",
    label: "Cloudflare",
    admin: true,
    cli: "dock cloudflare",
    words: "dns domains tunnel zone",
  },
  {
    to: "/$org/settings/registries",
    label: "Registries",
    admin: true,
    cli: "dock registry credentials",
    words: "registry credentials docker images pull",
  },
  {
    to: "/$org/settings/data-access",
    label: "Data access",
    admin: false,
    cli: "dock db policy show",
    words: "database policy data panel",
  },
  {
    to: "/$org/settings/archived",
    label: "Archived projects",
    admin: false,
    cli: "dock project list --all",
    words: "archive restore delete old projects",
  },
  {
    to: "/$org/settings/analytics",
    label: "Analytics defaults",
    admin: false,
    cli: "dock analytics",
    words: "consent privacy replays",
  },
] as const;

const RECENT = "dock.recent";

/** Ids of the last few commands run from the palette. */
export function recentIds(): string[] {
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(RECENT) ?? "[]");
    return Array.isArray(parsed)
      ? parsed.filter((id): id is string => typeof id === "string")
      : [];
  } catch {
    return [];
  }
}

export function rememberCommand(id: string) {
  const ids = [id, ...recentIds().filter((x) => x !== id)].slice(0, 3);
  localStorage.setItem(RECENT, JSON.stringify(ids));
}
