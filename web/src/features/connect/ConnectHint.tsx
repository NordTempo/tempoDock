import { useQuery } from "@tanstack/react-query";
import { api, body } from "../../api/client";
import { servicePath, useEnvironment } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { CopyButton } from "../../ui/kit";
import { openHelp } from "../help/open";

// `dock connect` stays a CLI action (ADR 0023, 0025): it opens a port on
// the user's machine. The UI shows the command for this service and
// whether the caller may run it, from GET .../connect (which audits
// nothing without reveal).

/** The `dock connect` line for a service; production needs --confirm. */
export const connectCli = (ref: ServiceRef, production: boolean) =>
  `dock connect ${ref.service} -p ${ref.project} -e ${ref.environment}${
    production ? ` --confirm ${ref.environment}` : ""
  }`;

/**
 * How to reach the service from your machine: the `dock connect` line and
 * whether you may run it, then (for Postgres, MySQL and Redis) the client's line,
 * each with Copy.
 */
export function ConnectHint({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const production = useEnvironment(ref).environment.kind === "production";
  const info = useQuery({
    queryKey: ["connect", ref.project, ref.environment, ref.service],
    queryFn: () =>
      body(
        api.GET(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/connect",
          {
            // The environment's name confirms production; others ignore it.
            params: {
              path: servicePath(ref),
              query: { confirm: ref.environment },
            },
          },
        ),
      ),
    retry: false,
  });
  const cli = connectCli(ref, production);
  const d = info.data;
  const psql =
    d?.engine === "postgres"
      ? `psql -h 127.0.0.1 -p ${d.port} -U ${d.user} ${d.database}`
      : d?.engine === "mysql"
        ? `mysql -h 127.0.0.1 -P ${d.port} -u ${d.user} -p ${d.database ?? ""}`.trimEnd()
        : d?.engine === "redis"
          ? `redis-cli -h 127.0.0.1 -p ${d.port} --user ${d.user ?? "default"} --askpass`
          : undefined;
  const row = (label: string, line: string) => (
    <div className="flex items-center gap-3">
      <span className="w-16 flex-none text-faint">{label}</span>
      <code className="min-w-0 truncate font-mono" title={line}>
        {line}
      </code>
      <CopyButton text={line} />
    </div>
  );
  return (
    <div className="flex flex-col gap-1.5 text-xs">
      {row("Connect", cli)}
      {psql && row("Then", psql)}
      <span className="pl-[76px]">
        {info.data ? (
          <span className="text-mut">
            <span className="text-ok">✓</span> You may run it: it opens
            localhost:{info.data.port} on your machine, reaching the service.{" "}
            <button
              type="button"
              className="underline underline-offset-3"
              onClick={() => openHelp("data")}
            >
              What's this?
            </button>
          </span>
        ) : info.error ? (
          <span className="text-warn">{info.error.message}</span>
        ) : (
          <span className="text-faint">Checking your access</span>
        )}
      </span>
    </div>
  );
}
