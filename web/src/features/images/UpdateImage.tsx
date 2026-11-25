import { useState } from "react";
import { useCanDeploy, useDeployBlock } from "../../data/queries";
import type { ServiceRef, ServiceView } from "../../data/types";
import { Act, Confirm } from "../../ui/kit";
import { useDeployImage } from "../service/FixAndDeploy";
import type { ImageScan } from "./data";

/** The base of a live image deployed by tag whose tag has a newer image. */
type Moved = NonNullable<ImageScan["base"]> & { latest: string };

/**
 * The live image's tag points at a newer image (ADR 0074, amendment
 * 2026-11-25): the base of an image deployed by tag, when the daily
 * check saw it move. Undefined otherwise.
 */
export function movedTag(scan: ImageScan | undefined): Moved | undefined {
  const base = scan?.base;
  return base?.own && base.moved && base.latest
    ? { ...base, latest: base.latest }
    : undefined;
}

/**
 * Update: deploys the tag's newer image by digest, after a Confirm. Shown
 * in NeedsAttention's rows; nothing when the caller can't deploy.
 */
export function UpdateImage({
  serviceRef: ref,
  view,
  base,
}: {
  serviceRef: ServiceRef;
  view: ServiceView;
  base: Moved;
}) {
  const mayDeploy = useCanDeploy(ref);
  const blocked = useDeployBlock(ref);
  const deploy = useDeployImage();
  const [asking, setAsking] = useState(false);
  if (!mayDeploy) return null;
  const image = `${base.image}@${base.latest}`;
  const short = `${base.image}@${base.latest.replace(/^sha256:/, "").slice(0, 7)}`;
  const cli = `dock deploy -s ${ref.service} -e ${ref.environment} --image ${image}`;
  return (
    <>
      <Act
        small
        cli={cli}
        disabled={!!blocked || !!view.deploying}
        title={blocked ?? `Deploys ${short}`}
        onClick={() => setAsking(true)}
      >
        Update
      </Act>
      {asking && (
        <Confirm
          title={`Update ${ref.service} to ${short}?`}
          action="Update"
          cli={cli}
          pending={deploy.isPending}
          error={deploy.error}
          onConfirm={() =>
            deploy.mutate({ ref, image }, { onSuccess: () => setAsking(false) })
          }
          onCancel={() => setAsking(false)}
        >
          Deploys the newest <b className="font-mono">{base.image}</b> to{" "}
          {ref.environment}. Same tag, so the data stays as it is.
          {view.service.stateful &&
            " The service restarts, so connections drop for a moment."}
        </Confirm>
      )}
    </>
  );
}
