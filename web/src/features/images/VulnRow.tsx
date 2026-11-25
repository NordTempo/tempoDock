import { Link, useNavigate } from "@tanstack/react-router";
import { useCanDeploy, useDeployBlock } from "../../data/queries";
import type { ServiceRef, ServiceView } from "../../data/types";
import { useShortcuts } from "../../shell/keys";
import { useOrg } from "../../shell/location";
import { Act } from "../../ui/kit";
import { openDeploy } from "../deploy/start";
import { Row } from "../service/Attention";
import type { ImageScan } from "./data";
import { Kev, total, WorstCounts, worstFixSentence } from "./parts";
import { movedTag, UpdateImage } from "./UpdateImage";

/**
 * The live image's critical or high vulnerabilities (ADR 0074) as a row
 * of the Overview's Needs attention list (ADR 0076): red with criticals,
 * amber with highs only. Redeploy (the Deploy window) when a rebuild
 * fixes the worst and Dock builds the image (one built in CI is rebuilt
 * there), Update when the image's tag has a newer image; See all (V)
 * opens the image's page.
 */
export function VulnRow({
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
  const flags = `-s ${ref.service} -e ${ref.environment}`;
  const moved = movedTag(scan);
  return (
    <Row
      tone={scan.critical > 0 ? "fail" : "warn"}
      action={
        <>
          {scan.worst_fix === "rebuild" && scan.built && mayDeploy && (
            <Act
              small
              cli={`dock deploy ${flags}`}
              disabled={!!blocked || !!view.deploying}
              title={
                blocked ??
                "A new build picks up the base image's fixed packages"
              }
              onClick={() => openDeploy(ref)}
            >
              Redeploy
            </Act>
          )}
          {moved && <UpdateImage serviceRef={ref} view={view} base={moved} />}
          <Link
            to="/$org/images/$id"
            params={{ org, id: String(scan.id) }}
            className="btn sm"
          >
            See all {total(scan)} <kbd>V</kbd>
          </Link>
        </>
      }
    >
      <WorstCounts counts={scan} /> vulnerabilities in the live image
      {scan.kev > 0 && <Kev />}{" "}
      <span className="text-mut">
        · {scan.fixable} fixable. {worstFixSentence(scan)}
      </span>
    </Row>
  );
}
