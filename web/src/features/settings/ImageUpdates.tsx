import { useState } from "react";
import { useCanDeploy } from "../../data/queries";
import type { ServiceRef, ServiceView } from "../../data/types";
import { ago } from "../../ui/format";
import { Button, ErrorText } from "../../ui/kit";
import { autofocus } from "../create/form";
import { useSetAutoUpdate } from "../images/actions";
import { OpenValue } from "./OpenValue";

/**
 * Settings, Deploys: whether a service deployed by tag updates to the
 * tag's newer image on its own (ADR 0074, mock A). Shown while its live
 * image follows a tag, or while automatic updates are on (to turn them
 * off). Applies at once, like the database settings.
 */
export function ImageUpdatesRow({
  serviceRef: ref,
  view,
}: {
  serviceRef: ServiceRef;
  view: ServiceView;
}) {
  const mayDeploy = useCanDeploy(ref);
  const save = useSetAutoUpdate();
  const [editing, setEditing] = useState(false);
  const base = view.scan?.base?.own ? view.scan.base : undefined;
  const auto = view.service.auto_update;
  if (!base && !auto) return null;
  const tag = base ? (
    <span className="font-mono">{base.image}</span>
  ) : (
    "its tag"
  );
  const cli = `dock image-updates ${auto ? "manual" : "auto"} -s ${ref.service} -e ${ref.environment}`;
  const set = (on: boolean) =>
    on === auto
      ? setEditing(false)
      : save.mutate({ ref, on }, { onSuccess: () => setEditing(false) });
  return (
    <>
      <dt>
        <span className="block text-sec">Image updates</span>
      </dt>
      <dd>
        {editing ? (
          <form
            aria-label="Edit image updates"
            className="flex flex-col gap-2"
            onSubmit={(e) => e.preventDefault()}
            onKeyDown={(e) => e.key === "Escape" && setEditing(false)}
          >
            <span className="flex flex-wrap items-center gap-2">
              <span className="seg">
                {[false, true].map((on) => (
                  <button
                    key={String(on)}
                    type="button"
                    ref={on === auto ? autofocus : undefined}
                    aria-pressed={on === auto}
                    disabled={save.isPending}
                    onClick={() => set(on)}
                  >
                    {on ? "Automatic" : "Manual"}
                  </button>
                ))}
              </span>
              <Button small onClick={() => setEditing(false)}>
                Cancel <kbd>Esc</kbd>
              </Button>
            </span>
            <span className="text-xs text-mut">
              Automatic: after the daily check finds a newer {tag}, Dock
              snapshots the volumes and deploys it. If the new release doesn't
              come up, the old one keeps serving. Applies at once.{" "}
              <span className="font-mono">{cli}</span>
            </span>
            <ErrorText error={save.error} />
          </form>
        ) : (
          <OpenValue
            label="Edit image updates"
            onOpen={mayDeploy ? () => setEditing(true) : undefined}
          >
            {auto ? (
              <>
                Automatic{" "}
                <span className="text-mut">
                  · deploys a newer {tag} after the daily check, snapshot first
                </span>
              </>
            ) : (
              <>
                Manual{" "}
                <span className="text-mut">
                  · Update shows when {tag} has a newer image
                </span>
              </>
            )}
            {base && (
              <span className="block text-mut">
                {!base.checked_at ? (
                  "Not checked yet"
                ) : (
                  <>
                    {base.moved && base.latest ? (
                      <span className="text-warn">
                        Newer image{" "}
                        <span className="font-mono">
                          {base.latest.replace(/^sha256:/, "").slice(0, 7)}
                        </span>
                      </span>
                    ) : (
                      "Up to date"
                    )}
                    {` · checked ${ago(base.checked_at)}`}
                  </>
                )}
              </span>
            )}
          </OpenValue>
        )}
      </dd>
    </>
  );
}
