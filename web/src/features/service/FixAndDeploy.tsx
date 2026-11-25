import { useState } from "react";
import { api, body } from "../../api/client";
import { serviceWide, useAction, useStageSettings } from "../../data/mutations";
import { servicePath, useChanges, useService } from "../../data/queries";
import type { Release, ServiceRef } from "../../data/types";
import { Button, Confirm, ErrorText } from "../../ui/kit";
import { invalid, useValidation } from "../create/form";
import { openDeploy } from "../deploy/start";
import { shortSha, useGitDeploy } from "../git/api";
import { imageLabel, shortImage } from "./releases";

// A failed release's likely fix, edited in place and deployed in one go:
// on the release page and on the service's Overview, where the failure
// is said with its fix (ADR 0099).

/** Deploys a given image (`dock deploy --image`), taking staged changes with it. */
export const useDeployImage = () =>
  useAction(
    ({ ref, image }: { ref: ServiceRef; image: string }) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/releases",
          { params: { path: servicePath(ref) }, body: { image } },
        ),
      ),
    ({ ref }) => serviceWide(ref),
    ({ ref }, r) => [
      `Deploy of ${ref.service} started as r${r.number}`,
      "info",
    ],
  );

/**
 * The likely fix, edited in place: the new port or health check path is
 * staged and the failed release's own image (or commit) deploys again
 * with it, so the retry is the release that failed, not the serving one.
 * An upload has nothing to redeploy; it stages and opens Deploy. Save only
 * stages it without deploying, to apply later.
 */
export function FixAndDeploy({
  serviceRef: ref,
  release,
  fix,
  onClose,
  onDeployed,
}: {
  serviceRef: ServiceRef;
  release: Release;
  /** The setting to change, and the value the diagnosis suggests. */
  fix: {
    edit: "port" | "health_check_path";
    label: string;
    suggested?: string;
  };
  onClose: () => void;
  onDeployed: () => void;
}) {
  const { service } = useService(ref);
  const changes = useChanges(ref);
  const stage = useStageSettings();
  const deployImage = useDeployImage();
  const gitDeploy = useGitDeploy(ref);
  const port = fix.edit === "port";
  const [value, setValue] = useState(
    fix.suggested ??
      (port ? String(release.config.port) : release.config.health_check_path),
  );
  const v = useValidation();
  const typed = value.trim();
  const problem = port
    ? /^\d+$/.test(typed) && Number(typed) >= 1 && Number(typed) <= 65535
      ? undefined
      : "Enter a port from 1 to 65535."
    : typed.startsWith("/")
      ? undefined
      : "Enter a path that starts with /.";
  const error = v.tried ? problem : undefined;
  // An image release deploys its image again, a git build its commit.
  const image = release.source === "image" ? release.image_ref : "";
  const commit = image ? undefined : release.commit;
  const what = commit
    ? shortSha(commit.sha)
    : image
      ? shortImage(imageLabel(release))
      : "";
  // Other staged changes of this service go out with the deploy too.
  const others =
    changes.settings.filter(
      (c) => c.service === ref.service && c.field !== fix.edit,
    ).length +
    changes.variables.filter((c) => c.service === ref.service).length +
    changes.volumes.filter((c) => c.service === ref.service).length;
  const flags = `-s ${ref.service} -e ${ref.environment}`;
  const setFlag = port ? `--port ${typed}` : `--health-check-path ${typed}`;
  const pending =
    stage.isPending || deployImage.isPending || gitDeploy.isPending;

  // Production-affecting like any deploy: asked first, in the one Confirm.
  const [asking, setAsking] = useState(false);
  const submit = () => {
    if (!v.check(!problem)) return;
    if ((commit || image) && !asking) return setAsking(true);
    setAsking(false);
    const patch = port ? { port: Number(typed) } : { health_check_path: typed };
    stage.mutate(
      { ref, patch },
      {
        onSuccess: () => {
          if (commit) gitDeploy.mutate(commit.sha, { onSuccess: onDeployed });
          else if (image)
            deployImage.mutate({ ref, image }, { onSuccess: onDeployed });
          else openDeploy(ref);
        },
      },
    );
  };
  // Or only save it, to apply later with other changes (no deploy now).
  const saveOnly = () => {
    if (!v.check(!problem)) return;
    const patch = port ? { port: Number(typed) } : { health_check_path: typed };
    stage.mutate({ ref, patch }, { onSuccess: onClose });
  };
  return (
    <form
      ref={v.form}
      aria-label={fix.label}
      className="flex flex-col gap-1.5"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.preventDefault();
          onClose();
        }
      }}
    >
      <div className="flex flex-wrap items-center gap-2">
        <label className="flex items-center gap-2">
          <span className="text-xs text-faint">
            {port ? "Port" : "Health check path"}
          </span>
          <input
            // biome-ignore lint/a11y/noAutofocus: opened by the button it replaces
            autoFocus
            value={value}
            inputMode={port ? "numeric" : undefined}
            aria-invalid={!!error}
            onChange={(e) => setValue(e.target.value)}
            className={`field w-40 py-[5px] font-mono ${invalid(error)}`}
            spellCheck={false}
          />
        </label>
        <button type="submit" className="btn" disabled={pending}>
          {commit || image ? "Save and deploy" : "Save and choose a folder"}{" "}
          <kbd>↵</kbd>
        </button>
        {(commit || image) && (
          <Button
            disabled={pending}
            title="Saves it as a change waiting to apply; nothing deploys now"
            onClick={saveOnly}
          >
            Save only
          </Button>
        )}
        <Button onClick={onClose}>
          Cancel <kbd>Esc</kbd>
        </Button>
        <span className="text-xs">
          {error ? (
            <span className="text-fail">{error}</span>
          ) : (
            <ErrorText
              error={stage.error ?? deployImage.error ?? gitDeploy.error}
            />
          )}
        </span>
      </div>
      {asking && (
        <Confirm
          title={`Deploy ${what} with ${port ? `port ${typed}` : `health check ${typed}`}?`}
          action={`Deploy ${what}`}
          cli={`dock service update ${ref.service} ${setFlag} -e ${ref.environment}`}
          pending={pending}
          onConfirm={submit}
          onCancel={() => setAsking(false)}
        >
          <p>
            Saves the {port ? "port" : "health check path"} and deploys {what}{" "}
            to {ref.environment} again as a new release.
          </p>
          {service.stateful && service.active_release ? (
            <p className="mt-2 font-semibold">
              {ref.service} is down for a few seconds while r
              {service.active_release} stops and the new release starts: only
              one copy can use its volume at a time.
            </p>
          ) : (
            service.active_release && (
              <p className="mt-2 text-mut">
                r{service.active_release} keeps serving until the new release
                passes its health check.
              </p>
            )
          )}
        </Confirm>
      )}
      {(commit || image) && (
        <p className="text-xs text-mut">
          Asks before it deploys {what} to {ref.environment} again.
        </p>
      )}
      {others > 0 && (
        <p className="text-xs text-mut">
          {ref.service}'s {others} other staged{" "}
          {others === 1 ? "change goes" : "changes go"} out with it.
        </p>
      )}
      <div className="flex flex-col font-mono text-2xs break-all text-faint">
        <code>
          dock service update {ref.service} {setFlag} -e {ref.environment}
        </code>
        <code>
          {commit
            ? `dock git deploy --ref ${commit.sha} ${flags}`
            : image
              ? `dock deploy --image ${image} ${flags}`
              : `dock deploy <dir> ${flags}`}
        </code>
      </div>
    </form>
  );
}
