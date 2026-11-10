import { Link, useNavigate } from "@tanstack/react-router";
import { useCanDeploy, useDeployBlock } from "../../data/queries";
import type { ServiceRef, ServiceView } from "../../data/types";
import { useShortcuts } from "../../shell/keys";
import { useOrg } from "../../shell/location";
import { Act } from "../../ui/kit";
import { openDeploy } from "../deploy/start";
import type { ImageScan } from "./data";
import { Kev, total, WorstCounts, worstFixSentence } from "./parts";

/**
 * The service overview's line about its live image (ADR 0074, mock O2):
 * a ruled strip under the status, only while the scan found critical or
 * high vulnerabilities. Red edge with criticals, amber with highs only.
 * Redeploy (the Deploy window) when a rebuild fixes the worst and Dock
 * builds the image (one built in CI is rebuilt there); See all
 * (V) opens the image's page.
 */
export function VulnStrip({
  serviceRef: ref,
  view,
}: {
  serviceRef: ServiceRef;
  view: ServiceView;
}) {
  const scan = view.scan;
  if (scan?.state !== "scanned" || (scan.critical === 0 && scan.high === 0))
    return null;
  return <Strip serviceRef={ref} view={view} scan={scan} />;
}

function Strip({
  serviceRef: ref,
  view,
  scan,
}: {
  serviceRef: ServiceRef;
  view: ServiceView;
  scan: ImageScan;
}) {
  const org = useOrg();
  const navigate = useNavigate();
  const mayDeploy = useCanDeploy(ref);
  const blocked = useDeployBlock(ref);
  useShortcuts([
    {
      key: "v",
      label: "vulnerabilities",
      run: () =>
        navigate({
          to: "/$org/images/$id",
          params: { org, id: String(scan.id) },
        }),
    },
  ]);
  const critical = scan.critical > 0;
  const flags = `-s ${ref.service} -e ${ref.environment}`;
  return (
    <div
      className={`flex flex-wrap items-center gap-x-3 gap-y-2 border-l-2 py-1.5 pr-1.5 pl-3 text-sm ${critical ? "border-fail bg-fail-bg" : "border-warn bg-warn-bg"}`}
    >
      <p className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="font-medium">
          <WorstCounts counts={scan} /> vulnerabilities in the live image
          {scan.kev > 0 && <Kev />}
        </span>
        <span className="text-mut">
          {scan.fixable} fixable. {worstFixSentence(scan)}
        </span>
      </p>
      <span className="flex flex-wrap items-center gap-2">
        {scan.worst_fix === "rebuild" && scan.built && mayDeploy && (
          <Act
            small
            cli={`dock deploy ${flags}`}
            disabled={!!blocked || !!view.deploying}
            title={
              blocked ?? "A new build picks up the base image's fixed packages"
            }
            onClick={() => openDeploy(ref)}
          >
            Redeploy
          </Act>
        )}
        <Link
          to="/$org/images/$id"
          params={{ org, id: String(scan.id) }}
          className="btn sm"
        >
          See all {total(scan)} <kbd>V</kbd>
        </Link>
      </span>
    </div>
  );
}
