import { useQueries } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import {
  useDeploy,
  useLogout,
  useRestart,
  useRollback,
} from "../data/mutations";
import {
  queries,
  serviceRef,
  useIsAdmin,
  useProjectTree,
  useServers,
} from "../data/queries";
import { rollbackTarget } from "../features/service/releases";
import { serviceState, type Tone } from "../ui/status";
import { useCurrentContext } from "./location";

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

const serviceTabs = [
  ["logs", "Logs", "g l"],
  ["metrics", "Metrics", "g m"],
  ["releases", "Releases", "g r"],
  ["domains", "Domains", "g d"],
  ["settings", "Settings", "g s"],
  ["audit", "Audit", "g a"],
] as const;

/** Every palette command, built from the project tree and current location. */
export function useCommands(): Command[] {
  const projects = useProjectTree();
  const here = useCurrentContext();
  const navigate = useNavigate();
  const deploy = useDeploy();
  const restart = useRestart();
  const rollback = useRollback();
  const logout = useLogout();
  const admin = useIsAdmin();
  const servers = useServers();

  // Rollback targets need release history, cached for the current project.
  const local = projects
    .filter((p) => p.project.name === here.project)
    .flatMap((p) => p.environments.flatMap((e) => e.services))
    .map((s) => serviceRef(s.service));
  const histories = useQueries({
    queries: local.map((ref) => ({
      ...queries.releases(ref),
      staleTime: 60_000,
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
      run: () => navigate({ to: "/$project", params: { project } }),
    });
    if (environments.length > 1) {
      add({
        id: `compare:${project}`,
        group: "Go to",
        label: `Compare ${project} environments`,
        strong: project,
        cli: inProject ? "C" : "",
        weight: inProject ? 1 : 0,
        run: () => navigate({ to: "/$project/compare", params: { project } }),
      });
    }

    for (const { environment: env, services } of environments) {
      const environment = env.name;
      const inEnv = inProject && environment === here.environment;
      add({
        id: `vars:${project}/${environment}`,
        group: "Go to",
        label: `Variables · ${project} ${environment}`,
        strong: environment,
        cli: `dock env list -e ${environment}${pFlag}`,
        weight: (inProject ? 1 : 0) + (inEnv ? 1 : 0),
        run: () =>
          navigate({
            to: "/$project/$environment",
            params: { project, environment },
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
          navigate({ to: "/$project/$environment/$service", params });
        const busy = svc.deploying
          ? {
              text: `r${svc.deploying.number} is deploying`,
              tone: "prog" as const,
            }
          : undefined;

        // Image deploys only; source deploys upload from the CLI.
        if (svc.service.image) {
          add({
            id: `deploy:${id}`,
            group: "Actions",
            label: `Deploy ${service} ${where}`,
            strong: service,
            note: busy ?? note,
            cli: `dock deploy${flags}`,
            weight,
            run: async () => {
              await deploy.mutateAsync(params);
              await open();
            },
          });
        }
        if (svc.service.active_release) {
          add({
            id: `restart:${id}`,
            group: "Actions",
            label: `Restart ${service} ${where}`,
            strong: service,
            note: busy ?? note,
            cli: `dock restart${flags}`,
            weight: weight - 0.5,
            run: () => restart.mutateAsync(params),
          });
        }
        const target = rollbackTarget(
          historyOf(project, environment, service) ?? [],
        );
        if (target) {
          add({
            id: `rollback:${id}`,
            group: "Actions",
            label: `Roll back ${service} ${where} to r${target.number}`,
            strong: service,
            note: busy ?? note,
            cli: `dock rollback ${target.number}${flags}`,
            weight: weight - 0.5,
            run: async () => {
              await rollback.mutateAsync({ ref: params, to: target.number });
              await open();
            },
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
        for (const [tab, title, keys] of serviceTabs) {
          add({
            id: `go:${id}:${tab}`,
            group: "Go to",
            label: `${title} · ${service} ${where}`,
            strong: service,
            cli: isHere ? keys.toUpperCase() : "",
            weight: weight - 1,
            run: () =>
              navigate({
                to: `/$project/$environment/$service/${tab}`,
                params,
              }),
          });
        }
      }
    }
  }

  const org = [
    ["Servers", "/servers", "dock server list"],
    ["Tokens", "/tokens", "dock token list"],
    ...(admin ? ([["Users", "/users", "dock user list"]] as const) : []),
    ["Audit log", "/audit", "dock audit"],
  ] as const;
  for (const [label, to, cli] of org) {
    add({
      id: `go:${to}`,
      group: "Go to",
      label,
      cli,
      weight: 0,
      run: () => navigate({ to }),
    });
  }
  // Server pages (statistics, placement) are for admins.
  for (const { name } of admin ? servers : []) {
    add({
      id: `go:server:${name}`,
      group: "Go to",
      label: `Server ${name}`,
      strong: name,
      cli: `dock server show ${name}`,
      weight: 0,
      run: () => navigate({ to: "/servers/$server", params: { server: name } }),
    });
  }
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

/** Ids of the last few commands run from the palette or command line. */
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
