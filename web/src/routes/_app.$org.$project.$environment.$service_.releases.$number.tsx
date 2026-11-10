import {
  createFileRoute,
  Link,
  notFound,
  useNavigate,
} from "@tanstack/react-router";
import { useState } from "react";
import { api, body } from "../api/client";
import { useServiceLogs } from "../data/live";
import { serviceWide, useAction, useStageSettings } from "../data/mutations";
import {
  findService,
  queries,
  servicePath,
  useCanDeploy,
  useChanges,
  useDeployBlock,
  useRelease,
  useReleases,
  useService,
} from "../data/queries";
import type { LogLine, Release, ServiceRef } from "../data/types";
import { invalid, useValidation } from "../features/create/form";
import { openDeploy } from "../features/deploy/start";
import { shortSha, useGitDeploy } from "../features/git/api";
import { hasFindings, notScanned, summary } from "../features/images/parts";
import {
  Cause,
  Evidence,
  fixEdit,
  useDiagnosis,
} from "../features/service/Diagnosis";
import { LogRow } from "../features/service/LogsPane";
import { RollbackConfirm } from "../features/service/ReleasesTable";
import {
  imageLabel,
  releaseDiff,
  servesCopyOf,
  shortImage,
} from "../features/service/releases";
import { deploySteps, StepTable } from "../features/service/Steps";
import { useShortcuts } from "../shell/keys";
import { useOrg } from "../shell/location";
import { ago, plural } from "../ui/format";
import {
  Act,
  Actor,
  actorKind,
  Button,
  Confirm,
  ErrorText,
  Glyph,
  Page,
  PageBar,
  Prop,
  SectionHead,
} from "../ui/kit";
import { failurePhrase, inFlight, releaseWhat } from "../ui/status";

// One release: why it failed (or how far it got), its steps, its last lines.
// Outside the service layout, like a document of its own: /shop/staging/web/releases/61.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service_/releases/$number",
)({
  loader: async ({ context: { queryClient }, params }) => {
    const number = Number(params.number);
    if (!Number.isInteger(number) || number < 1)
      throw notFound({ routeId: "/_app/$org" });
    const overview = await queryClient.ensureQueryData(
      queries.overview(params.project),
    );
    if (!findService(overview, params))
      throw notFound({ routeId: "/_app/$org" });
    const ref = servicePath(params);
    await Promise.all([
      queryClient.ensureQueryData(queries.release(ref, number)),
      queryClient.ensureQueryData(queries.releases(ref)),
    ]);
  },
  component: ReleasePage,
});

function ReleasePage() {
  const org = useOrg();
  const params = Route.useParams();
  const ref = servicePath(params);
  const view = useService(ref);
  const release = useRelease(ref, Number(params.number));
  const releases = useReleases(ref);
  const deploy = useDeployImage();
  const gitDeploy = useGitDeploy(ref);
  const mayDeploy = useCanDeploy(ref);
  const [rollingBack, setRollingBack] = useState(false);
  const blocked = useDeployBlock(ref);
  const navigate = useNavigate();
  const { service } = view;
  const serving = service.active_release ?? null;
  const servingRelease = useReleases(ref).find((x) => x.status === "active");
  const failed = release.status === "failed";
  const r = `r${release.number}`;
  // Its diagnosis (ADR 0071): the likely cause and the fix it points at.
  const diagnosis = useDiagnosis(ref, release);
  // Shown for a failed release, and for a live one that crash looped or
  // turned unhealthy.
  const diagnosed =
    diagnosis && (failed || diagnosis.kind !== "release")
      ? diagnosis
      : undefined;
  const edit = diagnosed ? fixEdit(diagnosed.cause.fix) : undefined;
  const fix =
    edit && "inPlace" in edit
      ? {
          edit: edit.inPlace,
          label: edit.label,
          suggested: diagnosed?.cause.fix?.suggested,
        }
      : undefined;
  // What it replaced (or would have): the release before it that served.
  const previous = releases.find(
    (x) =>
      x.number < release.number &&
      (x.status === "active" || x.status === "superseded"),
  );

  // Rolling back to what already serves would change nothing.
  const alreadyServing = servesCopyOf(servingRelease, release);
  const [fixing, setFixing] = useState(false);

  const older = releases.find((x) => x.number < release.number);
  const newer = [...releases].reverse().find((x) => x.number > release.number);
  const buildLog = { release: release.number, build: true };
  const toService = () =>
    navigate({
      to: "/$org/$project/$environment/$service",
      params: { org, ...ref },
    });
  useShortcuts([
    {
      key: "l",
      label: "build log",
      run: () =>
        navigate({
          to: "/$org/$project/$environment/$service/logs",
          params: { org, ...ref },
          search: buildLog,
        }),
    },
  ]);

  return (
    <>
      <PageBar
        parent={
          <span className="flex items-center gap-1.5">
            <Link
              to="/$org/$project"
              params={{ org, project: ref.project }}
              className="hover:text-fg"
            >
              {ref.project}
            </Link>
            <span aria-hidden className="text-faint">
              /
            </span>
            <Link
              to="/$org/$project/$environment"
              params={{
                org,
                project: ref.project,
                environment: ref.environment,
              }}
              className="hover:text-fg"
            >
              {ref.environment}
            </Link>
            <span aria-hidden className="text-faint">
              /
            </span>
            <Link
              to="/$org/$project/$environment/$service"
              params={{ org, ...ref }}
              className="hover:text-fg"
            >
              {ref.service}
            </Link>
          </span>
        }
        title={<span className="font-mono">r{release.number}</span>}
        actions={
          // The neighbours and the way back, so the page is never a dead end.
          <span className="flex items-center gap-3 text-xs">
            {older && (
              <Link
                to="/$org/$project/$environment/$service/releases/$number"
                params={{ org, ...ref, number: String(older.number) }}
                className="text-mut hover:text-fg"
              >
                ← r{older.number}
              </Link>
            )}
            {newer && (
              <Link
                to="/$org/$project/$environment/$service/releases/$number"
                params={{ org, ...ref, number: String(newer.number) }}
                className="text-mut hover:text-fg"
              >
                r{newer.number} →
              </Link>
            )}
            <Link
              to="/$org/$project/$environment/$service"
              params={{ org, ...ref }}
              className="btn sm"
            >
              All releases of {ref.service}
            </Link>
          </span>
        }
      />
      <Page>
        <section aria-labelledby="release" className="flex flex-col gap-3.5">
          <h2 id="release" className="text-xl leading-tight font-semibold">
            <Title release={release} />
          </h2>
          {failed && (
            <p className="text-md leading-normal">
              {serving && serving !== release.number ? (
                <>
                  <span className="text-ok">●</span>{" "}
                  <b className="font-semibold">
                    {/* Said as it was then; a later release may serve now. */}
                    {previous && previous.number !== serving
                      ? `r${previous.number} kept serving then; since, r${serving} took over.`
                      : `r${serving} is still serving.`}
                  </b>{" "}
                  <span className="text-mut">
                    {service.stateful
                      ? `${r} never got traffic.`
                      : `Users saw no change. ${r} was stopped and never got traffic.`}
                  </span>
                </>
              ) : (
                <span className="text-mut">
                  Nothing was serving before it, so nothing is serving now.
                </span>
              )}
            </p>
          )}
          <dl className="props grid-cols-[140px_minmax(0,1fr)]">
            <Prop label="Service">
              <span className="font-mono">{ref.service}</span> in{" "}
              {ref.environment}
            </Prop>
            <Prop label="Deployed by">
              <Actor
                name={release.created_by}
                kind={actorKind(release.created_by)}
              />{" "}
              · {ago(release.created_at)}
            </Prop>
            <Prop label="What">
              {releaseWhat(release)}{" "}
              <span className="font-mono">{release.image_ref}</span>
            </Prop>
            {release.scan && (
              <Prop label="Vulnerabilities">
                <ScanLine scan={release.scan} />
              </Prop>
            )}
            {previous && !diagnosed?.changes && (
              <Prop label={`Changed since r${previous.number}`}>
                <Changes from={previous} to={release} />
              </Prop>
            )}
          </dl>
          {diagnosed && <Cause d={diagnosed} />}
          {fix && fixing && (
            <FixAndDeploy
              release={release}
              fix={fix}
              onClose={() => setFixing(false)}
              onDeployed={toService}
            />
          )}
          <div className="mt-1 flex flex-wrap items-start gap-2.5">
            {fix &&
              !fixing &&
              (mayDeploy ? (
                <Button primary onClick={() => setFixing(true)}>
                  {fix.label}
                </Button>
              ) : (
                <Link
                  to="/$org/$project/$environment/$service/build"
                  params={{ org, ...ref }}
                  search={{ edit: fix.edit }}
                  className="btn pri"
                >
                  {fix.label}
                </Link>
              ))}
            {edit && !("inPlace" in edit) && (
              <Link
                {...("setting" in edit
                  ? {
                      to: "/$org/$project/$environment/$service/build",
                      search: { edit: edit.setting },
                    }
                  : { to: "/$org/$project/$environment/$service/variables" })}
                params={{ org, ...ref }}
                className="btn pri"
              >
                {edit.label}
              </Link>
            )}
            <Act
              // The next step only while it is building or after it failed;
              // on a healthy release, reading its log is just one option.
              primary={!edit && (failed || inFlight(release.status))}
              cli={`dock logs --build -r ${release.number} -s ${ref.service} -e ${ref.environment}`}
              onClick={() =>
                navigate({
                  to: "/$org/$project/$environment/$service/logs",
                  params: { org, ...ref },
                  search: buildLog,
                })
              }
            >
              Open build log <kbd>L</kbd>
            </Act>
            {failed && mayDeploy && (
              <DeployAgain
                release={release}
                // An image release retries its own image; others the setting.
                image={
                  release.source === "image" ? release.image_ref : service.image
                }
                disabled={
                  !!view.deploying ||
                  !!blocked ||
                  deploy.isPending ||
                  gitDeploy.isPending
                }
                title={blocked}
                onImage={(image) =>
                  deploy.mutate({ ref, image }, { onSuccess: toService })
                }
                onCommit={(sha) =>
                  gitDeploy.mutate(sha, { onSuccess: toService })
                }
                onOpen={() => openDeploy(ref)}
              />
            )}
            <Act
              cli={`dock env set -s ${ref.service} -e ${ref.environment}`}
              onClick={() =>
                navigate({
                  to: "/$org/$project/$environment/$service/variables",
                  params: { org, ...ref },
                })
              }
            >
              Variables of {ref.service}
            </Act>
            <Act
              cli={`dock service show ${ref.service} -e ${ref.environment}`}
              onClick={() =>
                navigate({
                  to: "/$org/$project/$environment/$service/build",
                  params: { org, ...ref },
                })
              }
            >
              Build &amp; run settings
            </Act>
            {release.status === "superseded" && mayDeploy && !rollingBack && (
              <Act
                cli={`dock rollback ${release.number} -s ${ref.service} -e ${ref.environment}`}
                disabled={alreadyServing || !!view.deploying || !!blocked}
                reason={
                  alreadyServing
                    ? `r${servingRelease?.number} already runs ${r}'s image and config`
                    : (blocked ??
                      (view.deploying
                        ? `r${view.deploying.number} is deploying`
                        : undefined))
                }
                onClick={() => setRollingBack(true)}
              >
                Roll back to {r}
              </Act>
            )}
            <span className="self-center">
              {failed && mayDeploy && blocked && (
                <span className="text-fail">Disabled: {blocked}. </span>
              )}
              <ErrorText error={deploy.error ?? gitDeploy.error} />
            </span>
          </div>
          {rollingBack && (
            <RollbackConfirm
              serviceRef={ref}
              target={release}
              serving={servingRelease}
              blocked={!!view.deploying || !!blocked}
              onDone={() => setRollingBack(false)}
            />
          )}
        </section>

        {diagnosed && <Evidence d={diagnosed} ref={ref} />}

        <section aria-labelledby="steps" className="flex flex-col gap-2">
          <SectionHead id="steps">
            {failed ? "What failed" : "Steps"}
          </SectionHead>
          {/* What served when it ran, not what serves now. */}
          <StepTable
            steps={deploySteps(
              release,
              previous?.number ?? null,
              service.stateful,
            )}
          />
        </section>

        <LastLines release={release} />
      </Page>
    </>
  );
}

/**
 * Retrying a failed release the way it was made: the same commit, the
 * service's image, or (an upload) the Deploy window for a new folder.
 */
function DeployAgain({
  release,
  image,
  disabled,
  title,
  onImage,
  onCommit,
  onOpen,
}: {
  release: Release;
  /** The service's image, for image services. */
  image: string | undefined;
  disabled: boolean;
  title: string | undefined;
  onImage: (image: string) => void;
  onCommit: (sha: string) => void;
  onOpen: () => void;
}) {
  const ref = servicePath(Route.useParams());
  const flags = `-s ${ref.service} -e ${ref.environment}`;
  // An image release with commit metadata still retries as an image.
  const commit = release.source === "image" ? undefined : release.commit;
  if (commit) {
    return (
      <Act
        cli={`dock git deploy --ref ${commit.sha} ${flags}`}
        disabled={disabled}
        title={title}
        onClick={() => onCommit(commit.sha)}
      >
        Deploy the commit again{" "}
        <span className="font-mono">{shortSha(commit.sha)}</span>
      </Act>
    );
  }
  if (image) {
    return (
      <Act
        cli={`dock deploy --image ${image} ${flags}`}
        disabled={disabled}
        title={title}
        onClick={() => onImage(image)}
      >
        Deploy {imageLabel({ image_ref: image, image_id: "" })} again
      </Act>
    );
  }
  return (
    <Act
      cli={`dock deploy <dir> ${flags}`}
      disabled={disabled}
      title={title}
      onClick={onOpen}
    >
      Deploy again
    </Act>
  );
}

function Title({ release }: { release: Release }) {
  const r = <span className="font-mono font-medium">r{release.number}</span>;
  switch (release.status) {
    case "failed":
      return (
        <>
          <span className="text-fail">✕</span> {r} {failurePhrase(release)}
        </>
      );
    case "active":
      return (
        <>
          <span className="text-ok">●</span> {r} is live
        </>
      );
    case "superseded":
      return <>{r} was replaced</>;
    default:
      return (
        <>
          <span className="text-prog">
            <Glyph char="◐" />
          </span>{" "}
          {r} is deploying
        </>
      );
  }
}

/** What differs between two releases, on one line. */
function Changes({ from, to }: { from: Release; to: Release }) {
  const changes = releaseDiff(from, to);
  if (changes.length === 0)
    return (
      <span className="text-mut">same image, settings and variable names</span>
    );
  return changes.map((c, i) => (
    <span key={c.what}>
      {i > 0 && " · "}
      {c.what === "added" || c.what === "removed" ? (
        <>
          {c.what} <span className="font-mono">{c.text}</span>
        </>
      ) : (
        c.text
      )}
    </span>
  ));
}

/** Deploys a given image (`dock deploy --image`), taking staged changes with it. */
const useDeployImage = () =>
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
function FixAndDeploy({
  release,
  fix,
  onClose,
  onDeployed,
}: {
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
  const ref = servicePath(Route.useParams());
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
        <button type="submit" className="btn pri" disabled={pending}>
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

/** The release's last container lines; for a failed one, why it stopped. */
function LastLines({ release }: { release: Release }) {
  const org = useOrg();
  const params = Route.useParams();
  const ref = servicePath(params);
  // A replaced or failed release's kept output is read once, not followed.
  const logs = useServiceLogs(ref, release.number, {
    tail: 500,
    follow: release.status === "active" || inFlight(release.status),
  });
  const failed = release.status === "failed";
  // Oldest first, repeats folded, then the last screenful.
  const rows = collapseRepeats(logs.lines.slice(0, 200).reverse()).slice(-14);
  return (
    <section aria-labelledby="lines" className="flex flex-col gap-2">
      <SectionHead
        id="lines"
        right={
          <Link
            to="/$org/$project/$environment/$service/logs"
            params={{ org, ...ref }}
            search={{ release: release.number }}
            className="text-mut"
          >
            Full log
          </Link>
        }
      >
        {failed ? `Last lines before r${release.number} stopped` : "Last lines"}
      </SectionHead>
      <div
        role="log"
        aria-label={`r${release.number} last log lines`}
        className={`overflow-x-auto border py-2.5 font-mono text-xs leading-[1.7] ${failed ? "border-fail-edge" : "border-line"}`}
      >
        {logs.error && <div className="px-3.5 text-fail">{logs.error}</div>}
        {rows.length === 0 && !logs.error && (
          <div className="px-3.5 text-mut">
            {logs.live || inFlight(release.status)
              ? "Waiting for output."
              : "No container output was kept."}
          </div>
        )}
        {rows.map((row) =>
          "repeat" in row ? (
            <div key={row.key} className="px-3.5 whitespace-pre text-mut">
              {"  "}… same {plural(row.size, "line")} × {row.repeat}
            </div>
          ) : (
            <LogRow key={row.key} line={row} quiet={failed} />
          ),
        )}
      </div>
    </section>
  );
}

type Row = LogLine | { key: string; size: number; repeat: number };

/** Lines alike up to their numbers (timestamps, counters, durations). */
const shape = (line: LogLine) => line.text.replace(/\d+/g, "#");

/**
 * Folds runs of a repeating line, or a repeating pair of lines, into the
 * first and last occurrence with "… same 2 lines × 14" between them.
 */
function collapseRepeats(lines: LogLine[]): Row[] {
  const out: Row[] = [];
  let i = 0;
  outer: while (i < lines.length) {
    for (const size of [1, 2]) {
      const block = (at: number) =>
        lines
          .slice(at, at + size)
          .map(shape)
          .join("\n");
      const first = block(i);
      let count = 1;
      while (
        i + (count + 1) * size <= lines.length &&
        block(i + count * size) === first
      ) {
        count++;
      }
      if (count >= 3) {
        out.push(...lines.slice(i, i + size));
        out.push({ key: `rep${i}`, size, repeat: count - 2 });
        out.push(...lines.slice(i + (count - 1) * size, i + count * size));
        i += count * size;
        continue outer;
      }
    }
    const line = lines[i];
    if (line) out.push(line);
    i++;
  }
  return out;
}

/**
 * The release's image scan in one line (ADR 0074): "2 critical, 5 high ·
 * 20 more" and the way to the image's page, or why it isn't scanned.
 */
function ScanLine({ scan }: { scan: NonNullable<Release["scan"]> }) {
  const org = useOrg();
  if (!hasFindings(scan))
    return (
      <span className="text-mut">
        {scan.state === "queued" || scan.state === "scanning"
          ? "Not scanned yet"
          : `Not scanned: ${notScanned(scan)}`}
      </span>
    );
  return (
    <span>
      <span
        className={
          scan.critical > 0 ? "text-fail" : scan.high > 0 ? "text-warn" : ""
        }
      >
        {summary(scan)}
      </span>{" "}
      ·{" "}
      <Link
        to="/$org/images/$id"
        params={{ org, id: String(scan.id) }}
        className="text-info hover:underline"
      >
        Image page
      </Link>
    </span>
  );
}
