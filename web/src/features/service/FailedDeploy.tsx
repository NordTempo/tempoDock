import { Link } from "@tanstack/react-router";
import { Suspense, useState } from "react";
import {
  useCanDeploy,
  useProjectOverview,
  useRelease,
} from "../../data/queries";
import type { ReleaseSummary, ServiceRef, ServiceView } from "../../data/types";
import { useOrg } from "../../shell/location";
import { ago } from "../../ui/format";
import { Button } from "../../ui/kit";
import { failurePhrase } from "../../ui/status";
import { servedElsewhere } from "../overview/pipeline";
import { fixEdit, useDiagnosis } from "./Diagnosis";
import { FixAndDeploy } from "./FixAndDeploy";
import { errorSummary, imageLabel, shortImage } from "./releases";

/**
 * A failed deploy that is still the news, said on the service's Overview
 * with its fix (ADR 0099), so nobody has to open the release to act: the
 * likely cause, the environment where the same image works and how its
 * settings differ, then the fix that fits (edited in place and deployed),
 * the logs and the release's own page for the rest. While the release
 * loads it is one line with the way to the release.
 */
export function FailedDeploy({
  serviceRef: ref,
  view,
  failed,
}: {
  serviceRef: ServiceRef;
  view: ServiceView;
  failed: ReleaseSummary;
}) {
  return (
    <section
      aria-labelledby="failed-deploy"
      className="flex flex-col gap-2.5 border-y border-line py-3"
    >
      <Heading serviceRef={ref} failed={failed} />
      <Suspense fallback={null}>
        <Body serviceRef={ref} view={view} failed={failed} />
      </Suspense>
    </section>
  );
}

/** "✕ r8 failed its health check · 21 min ago", and Details at the end. */
function Heading({
  serviceRef: ref,
  failed,
}: {
  serviceRef: ServiceRef;
  failed: ReleaseSummary;
}) {
  const org = useOrg();
  return (
    <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
      <h2 id="failed-deploy" className="text-md font-semibold text-fail">
        <span aria-hidden>✕ </span>r{failed.number} {failurePhrase(failed)}
      </h2>
      <span className="text-xs text-mut">{ago(failed.created_at)}</span>
      <Link
        to="/$org/$project/$environment/$service/history/$number"
        params={{ org, ...ref, number: String(failed.number) }}
        className="ml-auto text-xs text-mut hover:text-fg"
      >
        Details →
      </Link>
    </div>
  );
}

/** The cause, where the image works, and the buttons. */
function Body({
  serviceRef: ref,
  view,
  failed,
}: {
  serviceRef: ServiceRef;
  view: ServiceView;
  failed: ReleaseSummary;
}) {
  const org = useOrg();
  const release = useRelease(ref, failed.number);
  const diagnosis = useDiagnosis(ref, release);
  const overview = useProjectOverview(ref.project);
  const mayDeploy = useCanDeploy(ref);
  const [fixing, setFixing] = useState(false);
  const { service } = view;
  const where = servedElsewhere(overview.environments, view, failed);
  const there = where?.view.service.environment;
  const port = where?.diffs.find((d) => d.label === "port");
  // Nothing answered and no rule said why: the app most often listens on
  // another port than the one set, so the port is the setting to look at.
  const refused =
    failed.error_code === "health_check_failed" &&
    /connection refused/i.test(failed.error ?? "");
  const edit = diagnosis ? fixEdit(diagnosis.cause.fix) : undefined;
  // The diagnosis's fix first; else the port the working environment
  // uses; else, when nothing answered, the port to type.
  const fix =
    edit && "inPlace" in edit
      ? {
          edit: edit.inPlace,
          label: edit.label,
          suggested: diagnosis?.cause.fix?.suggested,
        }
      : port && there
        ? {
            edit: "port" as const,
            label: `Use ${there}'s port ${port.from}`,
            suggested: port.from,
          }
        : refused
          ? { edit: "port" as const, label: "Change port" }
          : undefined;
  const cause = diagnosis?.cause.rule
    ? diagnosis.cause.summary
    : failed.diagnosis?.rule
      ? failed.diagnosis.summary
      : refused
        ? `Nothing answered on :${service.port || 8080}.`
        : errorSummary(failed);
  const buildFailed = failed.error_code === "build_failed";
  return (
    <>
      {cause && <p>{cause}</p>}
      {where && there && (
        <div className="flex flex-col gap-1.5">
          <p className="text-sec">
            <span className="font-mono">{shortImage(imageLabel(failed))}</span>{" "}
            runs in {there} as r{where.view.active_release?.number}
            {where.diffs.length === 0
              ? `, with the same settings as ${ref.environment}.`
              : `. What ${there} sets differently:`}
          </p>
          {where.diffs.length > 0 && (
            <table className="w-auto text-xs">
              <thead>
                <tr>
                  <th>
                    <span className="sr-only">Setting</span>
                  </th>
                  <th className="font-normal text-mut">{there}</th>
                  <th className="font-normal text-mut">{ref.environment}</th>
                </tr>
              </thead>
              <tbody>
                {where.diffs.map((d) => (
                  <tr key={d.label}>
                    <td className="pr-8 text-mut">{d.label}</td>
                    <td className="pr-8 font-mono">{d.from}</td>
                    <td className="font-mono text-warn">{d.to}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
      {fix && fixing && (
        <FixAndDeploy
          serviceRef={ref}
          release={release}
          fix={fix}
          onClose={() => setFixing(false)}
          onDeployed={() => setFixing(false)}
        />
      )}
      <div className="flex flex-wrap items-center gap-2.5">
        {fix &&
          !fixing &&
          (mayDeploy ? (
            <Button onClick={() => setFixing(true)}>{fix.label}</Button>
          ) : (
            <Link
              to="/$org/$project/$environment/$service/settings"
              params={{ org, ...ref }}
              search={{ edit: fix.edit }}
              className="btn"
            >
              {fix.label}
            </Link>
          ))}
        {edit && !("inPlace" in edit) && (
          <Link
            {...("setting" in edit
              ? {
                  to: "/$org/$project/$environment/$service/settings",
                  search: { edit: edit.setting },
                }
              : { to: "/$org/$project/$environment/$service/variables" })}
            params={{ org, ...ref }}
            className="btn"
          >
            {edit.label}
          </Link>
        )}
        <Link
          to="/$org/$project/$environment/$service/logs"
          params={{ org, ...ref }}
          search={
            buildFailed
              ? { release: failed.number, build: true }
              : { release: failed.number }
          }
          className="btn"
        >
          {buildFailed ? "Open build log" : "Open logs"}
        </Link>
      </div>
    </>
  );
}
