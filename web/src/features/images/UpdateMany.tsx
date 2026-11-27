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
 * The services an image's Update deploys: its live uses that follow the
 * tag the scan follows, while it points at a newer image (base.own and
 * moved). Each gets the tag's newer image by digest. A scan is kept by
 * digest, so a use deployed by another tag of the same image (app:stable
 * beside app:latest) is left out: the newer image is another tag's.
 */
export function updateTargets(item: {
  scan?: ImageListItem["scan"];
  used_by?: readonly ImageUse[] | null;
}): UpdateTarget[] {
  const base = item.scan?.base;
  if (!base?.own || !base.moved || !base.latest) return [];
  const image = `${base.image}@${base.latest}`;
  return (item.used_by ?? [])
    .filter((u) => u.live && u.follows === base.image)
    .map((u) => ({
      ref: {
        project: u.project,
        environment: u.environment,
        service: u.service,
      },
      image,
    }));
}

/** Services named in the Confirm; the rest are counted. */
const LIST_CAP = 20;

const keyOf = (t: UpdateTarget) =>
  `${t.ref.project}/${t.ref.environment}/${t.ref.service}`;

/**
 * Update for one or more services from the Images pages (ADR 0074, mock
 * A): deploys each its tag's newer image by digest, one after another,
 * after a Confirm listing them. One that is refused doesn't stop the
 * rest; the Confirm stays open with what failed, and trying again sends
 * only those. Services the reader can't deploy are left out; with none
 * left it shows nothing.
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
  // Started in this Confirm: trying again skips them.
  const [started, setStarted] = useState<ReadonlySet<string>>(new Set());
  const targets = all.filter((t) => canDeploy(session, t.ref));
  if (targets.length === 0) return null;
  const one = targets.length === 1 ? targets[0] : undefined;
  const cli = one
    ? `dock deploy -p ${one.ref.project} -s ${one.ref.service} -e ${one.ref.environment} --image ${one.image}`
    : undefined;
  const run = async () => {
    setPending(true);
    setError(null);
    const done = new Set(started);
    const failed: string[] = [];
    for (const t of targets) {
      if (done.has(keyOf(t))) continue;
      try {
        await deploy.mutateAsync(t);
        done.add(keyOf(t));
      } catch (e) {
        failed.push(
          `${t.ref.service}: ${e instanceof Error ? e.message : String(e)}`,
        );
      }
    }
    setStarted(done);
    setPending(false);
    if (failed.length === 0) {
      setAsking(false);
      setStarted(new Set());
    } else setError(new Error(failed.join("; ")));
  };
  return (
    <>
      <Act
        small={small}
        cli={cli}
        onClick={() => {
          // A new Confirm starts over; only a retry within one skips.
          setStarted(new Set());
          setError(null);
          setAsking(true);
        }}
      >
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
            {targets.slice(0, LIST_CAP).map((t) => (
              <li key={keyOf(t)}>
                {t.ref.project} / {t.ref.environment} / {t.ref.service}{" "}
                <span className="text-mut">
                  {started.has(keyOf(t))
                    ? "started"
                    : `→ ${shortImage(t.image)}`}
                </span>
              </li>
            ))}
            {targets.length > LIST_CAP && (
              <li className="font-sans text-mut">
                and {targets.length - LIST_CAP} more
              </li>
            )}
          </ul>
        </Confirm>
      )}
    </>
  );
}
