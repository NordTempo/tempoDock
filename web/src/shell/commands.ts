import { useNavigate } from "@tanstack/react-router";
import { useDeploy, useLogout, useRestart } from "../data/mutations";
import { serviceRef, useProjectTree } from "../data/queries";
import { serviceStatus } from "../ui/status";
import { useCurrentContext } from "./location";

export type Command = {
  id: string;
  group: "Actions" | "Go to";
  label: string;
  hint?: string;
  /** The equivalent `dock` CLI invocation, shown in the palette footer. */
  cli: string;
  /** Context relevance: the current service beats the current project. */
  weight: number;
  run: () => unknown;
};

const serviceTabs = [
  ["logs", "dock logs"],
  ["domains", "dock domain list"],
  ["variables", "dock env list"],
  ["settings", "dock service show"],
] as const;

/** Every palette command, built from the project tree and current location. */
export function useCommands(): Command[] {
  const projects = useProjectTree();
  const here = useCurrentContext();
  const navigate = useNavigate();
  const deploy = useDeploy();
  const restart = useRestart();
  const logout = useLogout();

  const commands: Command[] = [];
  const add = (c: Command) => commands.push(c);

  for (const { project: p, environments } of projects) {
    const project = p.name;
    const inProject = project === here.project;
    const pFlag = inProject ? "" : ` -p ${project}`;
    add({
      id: `go:${project}`,
      group: "Go to",
      label: project,
      cli: `dock status${pFlag}`,
      weight: inProject ? 2 : 0,
      run: () => navigate({ to: "/$project", params: { project } }),
    });

    for (const { environment: env, services } of environments) {
      const environment = env.name;
      const inEnv = inProject && environment === here.environment;
      add({
        id: `vars:${project}/${environment}`,
        group: "Go to",
        label: `${project} / ${environment} / shared variables`,
        cli: `dock env list -e ${environment}${pFlag}`,
        weight: inProject ? 1 : 0,
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
        const weight =
          inEnv && service === here.service
            ? 4
            : (inProject ? 2 : 0) + (inEnv ? 1 : 0);
        const path = `${project} / ${environment} / ${service}`;
        // Outside the current project, actions name the project too.
        const name = inProject ? service : `${project} / ${service}`;
        const status = serviceStatus(svc);
        const hint = status.tone === "ok" ? undefined : status.label;
        const open = () =>
          navigate({ to: "/$project/$environment/$service", params });

        // Image deploys only; source deploys upload from the CLI.
        if (svc.service.image) {
          add({
            id: `deploy:${id}`,
            group: "Actions",
            label: `Deploy ${name} to ${environment}`,
            hint,
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
            label: `Restart ${name} in ${environment}`,
            cli: `dock restart${flags}`,
            weight: weight - 0.5,
            run: () => restart.mutateAsync(params),
          });
        }
        add({
          id: `go:${id}`,
          group: "Go to",
          label: path,
          hint,
          cli: `dock status${flags}`,
          weight,
          run: open,
        });
        if (svc.deploying) {
          const r = svc.deploying.number;
          add({
            id: `go:${id}:deploy`,
            group: "Go to",
            label: `${path} / deploy r${r}`,
            hint: "in progress",
            cli: `dock release show r${r}${flags}`,
            weight: weight + 1,
            run: open,
          });
        }
        for (const [tab, cli] of serviceTabs) {
          add({
            id: `go:${id}:${tab}`,
            group: "Go to",
            label: `${path} / ${tab}`,
            cli: `${cli}${flags}`,
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
    ["Users", "/users", "dock user list"],
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

export type Match = { command: Command; ranges: Array<[number, number]> };

/**
 * Every query word must appear in the label. Matches at word starts and
 * context weight rank higher. An empty query lists the most relevant items.
 */
export function matchCommands(commands: Command[], query: string): Match[] {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  const scored: Array<Match & { score: number }> = [];
  for (const command of commands) {
    const label = command.label.toLowerCase();
    const ranges: Array<[number, number]> = [];
    let score = command.weight;
    for (const word of words) {
      const at = label.indexOf(word);
      if (at < 0) break;
      ranges.push([at, at + word.length]);
      if (at === 0 || label[at - 1] === " ") score += 3;
      score -= at / 100;
    }
    if (ranges.length === words.length) scored.push({ command, ranges, score });
  }
  scored.sort(
    (a, b) =>
      groupOrder(a.command) - groupOrder(b.command) || b.score - a.score,
  );
  const limit = words.length ? 8 : 5;
  return [
    ...scored.filter((m) => m.command.group === "Actions").slice(0, limit),
    ...scored.filter((m) => m.command.group === "Go to").slice(0, limit),
  ];
}

const groupOrder = (c: Command) => (c.group === "Actions" ? 0 : 1);
