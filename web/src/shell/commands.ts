import { useNavigate } from "@tanstack/react-router";
import {
  useDeploy,
  useLogout,
  usePromote,
  usePruneBuildCache,
  useRestart,
} from "../data/mutations";
import { useProjects, useServers } from "../data/queries";
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
  ["releases", "dock release list"],
  ["logs", "dock logs"],
  ["domains", "dock domain list"],
  ["variables", "dock env list"],
  ["settings", "dock service show"],
] as const;

/** Every palette command, built from the project tree and current location. */
export function useCommands(): Command[] {
  const projects = useProjects();
  const servers = useServers();
  const here = useCurrentContext();
  const navigate = useNavigate();
  const deploy = useDeploy();
  const restart = useRestart();
  const promote = usePromote();
  const prune = usePruneBuildCache();
  const logout = useLogout();

  const commands: Command[] = [];
  const add = (c: Command) => commands.push(c);

  for (const project of projects) {
    const inProject = project.id === here.project?.id;
    const p = inProject ? "" : ` -p ${project.name}`;
    const projectId = project.id;
    add({
      id: `go:${projectId}`,
      group: "Go to",
      label: project.name,
      cli: `dock status${p}`,
      weight: inProject ? 2 : 0,
      run: () =>
        navigate({ to: "/projects/$projectId", params: { projectId } }),
    });

    for (const env of project.environments) {
      const inEnv = env.id === here.env?.id;
      add({
        id: `vars:${env.id}`,
        group: "Go to",
        label: `${project.name} / ${env.name} / variables`,
        cli: `dock env list -e ${env.name}${p}`,
        weight: inProject ? 1 : 0,
        run: () =>
          navigate({
            to: "/projects/$projectId/variables",
            params: { projectId },
            search: { env: env.name },
          }),
      });

      for (const svc of env.services) {
        const serviceId = svc.id;
        const flags = ` -s ${svc.name} -e ${env.name}${p}`;
        const weight =
          svc.id === here.service?.id
            ? 4
            : (inProject ? 2 : 0) + (inEnv ? 1 : 0);
        const path = `${project.name} / ${env.name} / ${svc.name}`;
        // Outside the current project, actions name the project too.
        const name = inProject ? svc.name : `${project.name} / ${svc.name}`;
        const status = serviceStatus(svc.state);
        const open = () =>
          navigate({ to: "/services/$serviceId", params: { serviceId } });

        add({
          id: `deploy:${serviceId}`,
          group: "Actions",
          label: `Deploy ${name} to ${env.name}`,
          hint: status.tone === "ok" ? undefined : status.label,
          cli: `dock deploy${flags}`,
          weight,
          run: async () => {
            await deploy.mutateAsync(serviceId);
            await open();
          },
        });
        add({
          id: `restart:${serviceId}`,
          group: "Actions",
          label: `Restart ${name} in ${env.name}`,
          cli: `dock restart${flags}`,
          weight: weight - 0.5,
          run: () => restart.mutateAsync(serviceId),
        });
        add({
          id: `go:${serviceId}`,
          group: "Go to",
          label: path,
          hint: status.tone === "ok" ? undefined : status.label,
          cli: `dock status${flags}`,
          weight,
          run: open,
        });
        if (svc.state.state === "deploying") {
          add({
            id: `go:${serviceId}:deploy`,
            group: "Go to",
            label: `${path} / deploy r${svc.state.release}`,
            hint: "in progress",
            cli: `dock release show r${svc.state.release}${flags}`,
            weight: weight + 1,
            run: open,
          });
        }
        for (const [tab, cli] of serviceTabs) {
          add({
            id: `go:${serviceId}:${tab}`,
            group: "Go to",
            label: `${path} / ${tab}`,
            cli: `${cli}${flags}`,
            weight: weight - 1,
            run: () =>
              tab === "releases"
                ? open()
                : navigate({
                    to: `/services/$serviceId/${tab}`,
                    params: { serviceId },
                  }),
          });
        }
      }
    }

    const names = project.environments.map((e) => e.name);
    if (names.includes("staging") && names.includes("production")) {
      const staging = project.environments.find((e) => e.name === "staging");
      add({
        id: `promote:${projectId}`,
        group: "Actions",
        label: `Promote ${project.name} staging to production`,
        hint: staging?.services
          .map((s) => `${s.name} r${s.release}`)
          .join(", "),
        cli: `dock promote staging production${p}`,
        weight: inProject ? 1 : 0,
        run: async () => {
          await promote.mutateAsync({
            projectId,
            from: "staging",
            to: "production",
          });
          await navigate({ to: "/projects/$projectId", params: { projectId } });
        },
      });
    }
  }

  for (const server of servers) {
    if (server.disk.prunableGb > 0) {
      add({
        id: `prune:${server.id}`,
        group: "Actions",
        label: `Prune build cache on ${server.name}`,
        hint: `${server.disk.prunableGb} GB`,
        cli: `dock server prune ${server.name}`,
        weight: 0,
        run: () => prune.mutateAsync(server.id),
      });
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
    run: async () => {
      await logout.mutateAsync();
      await navigate({ to: "/login" });
    },
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
