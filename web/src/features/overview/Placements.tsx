import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { usePinPlacement } from "../../data/mutations";
import {
  useIsAdmin,
  usePlacements,
  useProjectOverview,
  useServers,
} from "../../data/queries";
import type { EnvironmentKind } from "../../data/types";
import { Button, ErrorText, Panel, SectionHead } from "../../ui/kit";
import { kindLabel } from "../../ui/status";

const kinds: EnvironmentKind[] = ["production", "non_production"];

/**
 * Which server each kind of the project's environments lives on. A kind
 * with nothing placed yet goes to the emptiest eligible server on its
 * first environment; admins can pin it before that.
 */
export function Placements({ project }: { project: string }) {
  const placements = usePlacements(project);
  const { environments } = useProjectOverview(project);
  const admin = useIsAdmin();
  return (
    <section aria-labelledby="placement" className="min-w-0">
      <SectionHead id="placement" aside="each kind stays on one server">
        Placement
      </SectionHead>
      <Panel>
        <table>
          <thead>
            <tr>
              <th>Kind</th>
              <th>Server</th>
              <th>Environments</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {kinds.map((kind) => {
              const placed = placements.find((p) => p.kind === kind);
              const envs = environments
                .filter((e) => e.environment.kind === kind)
                .map((e) => e.environment.name);
              return (
                <tr key={kind}>
                  <td>{kindLabel[kind]}</td>
                  <td className="font-mono">
                    {placed ? (
                      admin ? (
                        <Link
                          to="/servers/$server"
                          params={{ server: placed.server }}
                        >
                          {placed.server}
                        </Link>
                      ) : (
                        placed.server
                      )
                    ) : (
                      <span className="font-sans text-mut">not placed yet</span>
                    )}
                  </td>
                  <td className="font-mono">
                    {envs.join(", ") || (
                      <span className="font-sans text-mut">none</span>
                    )}
                  </td>
                  <td className="w-full whitespace-normal text-mut">
                    {placed ? (
                      placed.pinned ? (
                        "pinned by an admin"
                      ) : (
                        "chosen automatically"
                      )
                    ) : admin ? (
                      <Pin project={project} kind={kind} />
                    ) : (
                      "The first one goes to the server with the most free memory."
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </Panel>
    </section>
  );
}

/** Admin only: choose the server before the first environment of a kind. */
function Pin({ project, kind }: { project: string; kind: EnvironmentKind }) {
  const servers = useServers().filter((s) =>
    kind === "production" ? s.accepts_production : s.accepts_non_production,
  );
  const pin = usePinPlacement();
  const [server, setServer] = useState("");
  const words = kindLabel[kind];
  if (servers.length === 0) {
    return (
      <span>
        No server accepts {words} environments.{" "}
        <Link to="/servers">Servers</Link>
      </span>
    );
  }
  return (
    <span className="inline-flex flex-wrap items-center gap-2">
      <span>Automatic, or pin to</span>
      <select
        aria-label={`Server for ${words} environments`}
        value={server}
        onChange={(e) => setServer(e.target.value)}
        className="field py-[3px]"
      >
        <option value="">choose a server</option>
        {servers.map((s) => (
          <option key={s.id} value={s.name}>
            {s.name}
            {s.status === "offline" ? " (offline)" : ""}
            {s.disabled_for_placement ? " (no new placements)" : ""}
          </option>
        ))}
      </select>
      <Button
        small
        disabled={!server || pin.isPending}
        onClick={() => pin.mutate({ project, kind, server })}
      >
        Pin
      </Button>
      <code className="font-mono text-2xs">
        dock project pin {project} --kind {words} --server {server || "<name>"}
      </code>
      <ErrorText error={pin.error} />
    </span>
  );
}
