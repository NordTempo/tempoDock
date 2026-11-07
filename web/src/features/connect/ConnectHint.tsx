import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { api, body } from "../../api/client";
import { servicePath, useEnvironment } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { CopyButton } from "../../ui/kit";

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
 * whether you may run it, then (for Postgres) the psql line, each with Copy.
 */
export function ConnectHint({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const org = useOrg();
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
  const psql =
    info.data?.engine === "postgres"
      ? `psql -h 127.0.0.1 -p ${info.data.port} -U ${info.data.user} ${info.data.database}`
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
            <Link
              to="/$org/help"
              params={{ org }}
              hash="connect"
              className="underline underline-offset-3"
            >
              What's this?
            </Link>
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
