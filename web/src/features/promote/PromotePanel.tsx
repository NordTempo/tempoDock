import { useQueries } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { usePromote } from "../../data/mutations";
import { queries } from "../../data/queries";
import type {
  EnvironmentView,
  Promotion,
  SkippedService,
} from "../../data/types";
import { useOrg } from "../../shell/location";
import { Button, Check, ErrorText, Panel, StateText } from "../../ui/kit";
import { inFlight, releaseState, stepPhrase } from "../../ui/status";
import { imageLabel } from "../service/releases";
import { deploySteps, StepTable } from "../service/Steps";
import { type PlanRow, promoteCli } from "./plan";

/** Why the API skipped a service, when it sends no message. */
const skipReasons: Record<SkippedService["reason"], string> = {
  missing_in_target: "not in the target environment",
  no_active_release: "nothing active in the source",
  no_digest: "image not in the registry; redeploy it in the source first",
  deploy_in_progress: "a deploy is in progress there",
  failed: "stopped by a release that couldn't start",
};

const servicesWord = (n: number) => (n === 1 ? "service" : "services");

/**
 * The promote dialog under the compare header: what goes over and what is
 * skipped, the services to include, and for production a typed
 * confirmation. After it starts, the live progress of each new release.
 */
export function PromotePanel({
  project,
  from,
  to,
  plan,
  onClose,
}: {
  project: string;
  from: EnvironmentView;
  to: EnvironmentView;
  plan: PlanRow[];
  onClose: () => void;
}) {
  const promote = usePromote();
  const goes = plan.filter((r) => !r.skip);
  const [picked, setPicked] = useState(
    () => new Set(goes.map((r) => r.service)),
  );
  const [typed, setTyped] = useState("");
  const source = from.environment.name;
  const target = to.environment.name;

  if (promote.data) {
    return (
      <Progress
        project={project}
        to={to}
        from={source}
        result={promote.data}
        onDone={onClose}
      />
    );
  }

  const services = goes.map((r) => r.service).filter((s) => picked.has(s));
  // Services skipped here are left out of the request, so name the rest
  // unless the plan has nothing to skip.
  const all = services.length === plan.length;
  const cli = promoteCli(source, target, all ? undefined : services);
  const production = to.environment.kind === "production";
  const confirmed = !production || typed === target;
  const ready = services.length > 0 && confirmed && !promote.isPending;
  const start = () =>
    promote.mutate({ project, from: source, to: target, services });

  return (
    <section
      aria-labelledby="promote"
      className="flex flex-col gap-3 border-y border-line py-3"
    >
      <h2 id="promote" className="text-md font-semibold">
        Promote {services.length} {servicesWord(services.length)} from {source}{" "}
        to {target}
      </h2>
      <p className="text-mut">
        Each runs the exact image {source} runs now, with {target}'s own
        variables and settings, one service at a time with the usual health
        check. It stops at the first failure.
      </p>
      <Panel>
        <table>
          <thead>
            <tr>
              <th>Service</th>
              <th>{source}</th>
              <th>{target} now</th>
              <th className="w-full">Promote</th>
            </tr>
          </thead>
          <tbody>
            {plan.map((row) => (
              <tr key={row.service} className={row.skip ? "text-mut" : ""}>
                <td className="font-medium">{row.service}</td>
                <td className="font-mono">
                  {row.release
                    ? `r${row.release.number} ${imageLabel(row.release)}`
                    : "none"}
                </td>
                <td className="font-mono">
                  {row.current
                    ? `r${row.current.number} ${imageLabel(row.current)}`
                    : "none"}
                </td>
                <td>
                  {row.skip ? (
                    <span>skip, {row.skip}</span>
                  ) : (
                    <Check
                      label="yes"
                      checked={picked.has(row.service)}
                      onChange={(on) =>
                        setPicked((p) => {
                          const next = new Set(p);
                          if (on) next.add(row.service);
                          else next.delete(row.service);
                          return next;
                        })
                      }
                    />
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Panel>
      <form
        aria-label="Confirm promote"
        className="flex flex-wrap items-center gap-x-3 gap-y-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (ready) start();
        }}
      >
        {production && (
          <label className="flex items-center gap-2">
            <span>
              Type <b className="font-mono font-medium">{target}</b> to confirm
            </span>
            <input
              // biome-ignore lint/a11y/noAutofocus: the confirmation is the next thing to do
              autoFocus
              spellCheck={false}
              autoComplete="off"
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              className="field w-40 py-[3px] font-mono"
            />
          </label>
        )}
        <Button primary small type="submit" disabled={!ready}>
          Promote {services.length} {servicesWord(services.length)}
        </Button>
        <Button small onClick={onClose}>
          Cancel
        </Button>
        <code className="font-mono text-2xs text-mut">{cli}</code>
        <ErrorText error={promote.error} />
      </form>
    </section>
  );
}

/** The releases a promote created, live, and what it skipped. */
function Progress({
  project,
  from,
  to,
  result,
  onDone,
}: {
  project: string;
  from: string;
  to: EnvironmentView;
  result: Promotion;
  onDone: () => void;
}) {
  const org = useOrg();
  const target = to.environment.name;
  const promoted = result.promoted ?? [];
  const skipped = result.skipped ?? [];
  // Release events patch these as the deploys move (data/live.ts).
  const releases = useQueries({
    queries: promoted.map(({ service, release }) =>
      queries.release(
        { project, environment: target, service },
        release.number,
      ),
    ),
    combine: (results) => results.map((r, i) => r.data ?? promoted[i]?.release),
  });
  const rows = promoted.flatMap(({ service }, i) => {
    const release = releases[i];
    return release ? [{ service, release }] : [];
  });
  const busy = rows.some((r) => inFlight(r.release.status));
  const failed = rows.find((r) => r.release.status === "failed");
  // The steps of the one in flight, else the one that failed.
  const focus = rows.find((r) => inFlight(r.release.status)) ?? failed;
  const twin =
    focus && to.services.find((s) => s.service.name === focus.service);

  return (
    <section
      aria-labelledby="promote"
      className="flex flex-col gap-3 border-y border-line py-3"
    >
      <h2 id="promote" className="text-md font-semibold">
        {busy ? "Promoting" : failed ? "Promote stopped" : "Promoted"}{" "}
        {rows.length} {servicesWord(rows.length)} from {from} to {target}
      </h2>
      {rows.length > 0 && (
        <Panel>
          <table>
            <tbody>
              {rows.map(({ service, release }) => (
                <tr key={service}>
                  <td className="font-medium">{service}</td>
                  <td className="font-mono">
                    <Link
                      to="/$org/$project/$environment/$service/releases/$number"
                      params={{
                        org,
                        project,
                        environment: target,
                        service,
                        number: String(release.number),
                      }}
                    >
                      r{release.number}
                    </Link>
                  </td>
                  <td className="font-mono text-mut">{imageLabel(release)}</td>
                  <td className="w-full">
                    <StateText state={releaseState(release.status)} />
                    {inFlight(release.status) && (
                      <span className="text-mut">
                        , {stepPhrase[release.status]}
                      </span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </Panel>
      )}
      {focus && twin && (
        <StepTable
          steps={deploySteps(
            focus.release,
            twin.service.active_release ?? null,
            twin.service.stateful,
          )}
        />
      )}
      {skipped.length > 0 && (
        <p>
          <span className="text-mut">Skipped:</span>{" "}
          {skipped.map((s, i) => (
            <span key={s.service}>
              {i > 0 && "; "}
              <span className="font-medium">{s.service}</span>{" "}
              <span className="text-mut">
                ({s.message || skipReasons[s.reason]})
              </span>
            </span>
          ))}
        </p>
      )}
      <div>
        <Button small onClick={onDone}>
          {busy ? "Hide" : "Done"}
        </Button>
      </div>
    </section>
  );
}
