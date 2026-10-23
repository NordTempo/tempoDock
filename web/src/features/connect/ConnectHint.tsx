import { useQuery } from "@tanstack/react-query";
import { api, body } from "../../api/client";
import { servicePath, useEnvironment } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
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

/** One line: the command, a copy button, and the caller's access. */
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
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-xs">
      <span className="text-faint">From your machine</span>
      <code className="font-mono">{cli}</code>
      <CopyButton text={cli} />
      <span className="min-w-0 flex-[1_1_320px] whitespace-normal">
        {info.data ? (
          <>
            <span className="text-ok">✓</span>{" "}
            <span className="text-mut">
              you may connect;{" "}
              {info.data.engine === "postgres" ? (
                <>
                  then{" "}
                  <code className="font-mono text-sec">
                    psql -h 127.0.0.1 -p {info.data.port} -U {info.data.user}{" "}
                    {info.data.database}
                  </code>
                </>
              ) : (
                `it reaches port ${info.data.port}`
              )}
            </span>
          </>
        ) : info.error ? (
          <span className="text-warn">{info.error.message}</span>
        ) : (
          <span className="text-faint">checking your access</span>
        )}
      </span>
    </div>
  );
}
