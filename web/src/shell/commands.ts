import { useQueries } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useLogout } from "../data/mutations";
import {
  canDeploy,
  deployBlock,
  queries,
  serviceRef,
  useIsOperator,
  useProjectTreeIf,
  useServers,
  useSession,
} from "../data/queries";
import {
  canCreateEnvironment,
  canCreateProject,
} from "../features/create/access";
import { openCreate } from "../features/create/open";
import { openDeploy } from "../features/deploy/start";
import { requestLock } from "../features/overview/lockRequest";
import { promoteCli, promotionPairs } from "../features/promote/plan";
import { rollbackTarget } from "../features/service/releases";
import { tabsFor } from "../features/service/tabs";
import { serviceState, type Tone } from "../ui/status";
import { openConfirm } from "./confirmHost";
import { openKeyHelp } from "./KeyHelp";
import { useCurrentContext, useOrg } from "./location";
import { useShellMode } from "./Sidebar";

export type Command = {
  id: string;
  group: "Actions" | "Go to";
  /** Plain text, matched against the query. */
  label: string;
  /** A part of the label shown bold, usually the service name. */
  strong?: string;
  /** A short note after the label, like "↻ crash looping now". */
  note?: { text: string; tone: Tone };
  /** The equivalent `dock` CLI invocation, or the page's key sequence. */
  cli: string;
  /** Context relevance: the current service beats the current project. */
  weight: number;
  run: () => unknown;
};

/**
 * Every palette command, built from the project tree and current location.
 * Rollback commands need release history, which is fetched only while
 * `withHistory` (the palette open); until then
 * only histories already cached count.
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
  const operator = useIsOperator();
  const session = useSession();
  const servers = useServers();
  const blockOf = (server: string) =>
    deployBlock(servers.find((s) => s.name === server));

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
      const blocked = blockOf(to.environment.server);
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
      const blocked = blockOf(env.server);
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
            openCreate({ kind: "service", env: ref, source: "database" }),
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
            to: "/$org/$project/settings/variables/$environment",
            params: { org, project, environment },
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
        for (const tab of tabsFor(svc)) {
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

  // Operators reach the platform console's pages; everyone else the
  // activity they may see. Admins reach their organization's members and
  // tokens; everyone its general settings and variables.
  const pages = [
    ...(operator
      ? ([
          ["Platform console", "/console", ""],
          ["Servers", "/console/servers", "dock server list"],
          ["Registry", "/console/registry", "dock registry"],
          ["Storage", "/console/storage", "dock storage"],
          ["Backups", "/console/backups", "dock backup status"],
          ["Users", "/console/users", "dock user list"],
          ["Data access policy", "/console/data-access", "dock db policy show"],
          ["Alerts", "/console/alerts", "dock alerts"],
          ["Activity", "/console/audit", "dock audit"],
          ["GitHub App", "/console/github", "dock github"],
          ["Cloudflare", "/console/cloudflare", "dock cloudflare"],
          ["Address", "/console/address", "dock address"],
          ["Updates", "/console/updates", "dock update"],
          ["Analytics store", "/console/analytics", "dock analytics"],
        ] as const)
      : []),
    ["Account", "/account", "dock whoami"],
  ] as const;
  for (const [label, to, cli] of pages) {
    add({
      id: `go:${to}`,
      group: "Go to",
      label,
      cli,
      weight: 0,
      run: () => navigate({ to }),
    });
  }
  const orgPages = [
    ...(session?.scope === "admin"
      ? ([
          ["Members", "/$org/settings/members", "dock org members"],
          ["Tokens", "/$org/settings/tokens", "dock token list"],
          ["Requests", "/$org/requests", "dock request list"],
          [
            "Registry credentials",
            "/$org/settings/registries",
            "dock registry credentials",
          ],
        ] as const)
      : []),
    ["Analytics", "/$org/analytics", "dock analytics sites"],
    ["Organization variables", "/$org/settings/variables", "dock org env list"],
    ["Organization settings", "/$org/settings/general", "dock org list"],
    ...(operator ? [] : ([["Activity", "/$org/audit", "dock audit"]] as const)),
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
  // Server pages (statistics, placement) are in the console.
  for (const { name } of operator ? servers : []) {
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
    label: "Help: what Dock's words mean",
    cli: "",
    weight: -1,
    run: () => navigate({ to: "/$org/help", params: { org } }),
  });
  add({
    id: "keys",
    group: "Go to",
    label: "Keyboard shortcuts",
    cli: "?",
    weight: -1,
    run: openKeyHelp,
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
    const text = `${command.label} ${command.cli}`.toLowerCase();
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
      groupOrder(a.command) - groupOrder(b.command) || b.score - a.score,
  );
  const limit = words.length ? 6 : 4;
  return [
    ...scored.filter((m) => m.command.group === "Actions").slice(0, limit),
    ...scored.filter((m) => m.command.group === "Go to").slice(0, limit),
  ];
}

const groupOrder = (c: Command) => (c.group === "Actions" ? 0 : 1);

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
