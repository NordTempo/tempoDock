import { useState } from "react";
import { canDeploy, useSession } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { Act, Confirm } from "../../ui/kit";
import { useDeployImage } from "../service/FixAndDeploy";
import type { ImageListItem, ImageUse } from "./data";

/** A service to update and the image (tag@digest) it gets. */
export type UpdateTarget = { ref: ServiceRef; image: string };

/** "postgres:17-alpine@f9cf3e1", as images are shown elsewhere. */
export const shortImage = (image: string) => {
  const [name, digest] = image.split("@sha256:");
  return digest ? `${name}@${digest.slice(0, 7)}` : image;
};

/**
 * The services an image's Update deploys: its live uses whose image
 * follows a tag that points at a newer image (base.own and moved). Each
 * gets the tag's newer image by digest.
 */
export function updateTargets(item: {
  scan?: ImageListItem["scan"];
  used_by?: readonly ImageUse[] | null;
}): UpdateTarget[] {
  const base = item.scan?.base;
  if (!base?.own || !base.moved || !base.latest) return [];
  const image = `${base.image}@${base.latest}`;
  return (item.used_by ?? [])
    .filter((u) => u.live)
    .map((u) => ({
      ref: {
        project: u.project,
        environment: u.environment,
        service: u.service,
      },
      image,
    }));
}

/**
 * Update for one or more services from the Images pages (ADR 0074, mock
 * A): deploys each its tag's newer image by digest, one after another,
 * after a Confirm listing them. Services the reader can't deploy are left
 * out; with none left it shows nothing.
 */
export function UpdateMany({
  targets: all,
  label,
  small,
}: {
  targets: readonly UpdateTarget[];
  label: string;
  small?: boolean;
}) {
  const session = useSession();
  const deploy = useDeployImage();
  const [asking, setAsking] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const [pending, setPending] = useState(false);
  const targets = all.filter((t) => canDeploy(session, t.ref));
  if (targets.length === 0) return null;
  const one = targets.length === 1 ? targets[0] : undefined;
  const cli = one
    ? `dock deploy -p ${one.ref.project} -s ${one.ref.service} -e ${one.ref.environment} --image ${one.image}`
    : undefined;
  const run = async () => {
    setPending(true);
    setError(null);
    try {
      for (const t of targets) await deploy.mutateAsync(t);
      setAsking(false);
    } catch (e) {
      setError(e instanceof Error ? e : new Error(String(e)));
    } finally {
      setPending(false);
    }
  };
  return (
    <>
      <Act small={small} cli={cli} onClick={() => setAsking(true)}>
        {label}
      </Act>
      {asking && (
        <Confirm
          title={
            one
              ? `Update ${one.ref.service} to ${shortImage(one.image)}?`
              : `Update ${targets.length} services?`
          }
          action="Update"
          cli={cli}
          pending={pending}
          error={error}
          onConfirm={run}
          onCancel={() => setAsking(false)}
        >
          Deploys each the newest image of the tag it follows. Same tag, so data
          stays as it is; a service with a volume restarts, so its connections
          drop for a moment.
          <ul className="mt-2 flex flex-col font-mono">
            {targets.map((t) => (
              <li
                key={`${t.ref.project}/${t.ref.environment}/${t.ref.service}`}
              >
                {t.ref.project} / {t.ref.environment} / {t.ref.service}{" "}
                <span className="text-mut">→ {shortImage(t.image)}</span>
              </li>
            ))}
          </ul>
        </Confirm>
      )}
    </>
  );
}
