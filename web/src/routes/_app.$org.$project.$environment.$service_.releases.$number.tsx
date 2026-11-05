import {
  createFileRoute,
  Link,
  notFound,
  useNavigate,
} from "@tanstack/react-router";
import { useState } from "react";
import { useServiceLogs } from "../data/live";
import { useDeploy } from "../data/mutations";
import {
  findService,
  queries,
  servicePath,
  useCanDeploy,
  useDeployBlock,
  useRelease,
  useReleases,
  useService,
} from "../data/queries";
import type { LogLine, Release } from "../data/types";
import { openDeploy } from "../features/deploy/start";
import { shortSha, useGitDeploy } from "../features/git/api";
import { LogRow } from "../features/service/LogsPane";
import { RollbackConfirm } from "../features/service/ReleasesTable";
import {
  imageLabel,
  refusedPort,
  releaseDiff,
} from "../features/service/releases";
import { deploySteps, StepTable } from "../features/service/Steps";
import { useShortcuts } from "../shell/keys";
import { useOrg } from "../shell/location";
import { ago, plural } from "../ui/format";
import {
  Act,
  Actor,
  actorKind,
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
  const deploy = useDeploy();
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
  // What it replaced (or would have): the release before it that served.
  const previous = releases.find(
    (x) =>
      x.number < release.number &&
      (x.status === "active" || x.status === "superseded"),
  );

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
              to="/$org/$project/$environment/$service"
              params={{ org, ...ref }}
              className="hover:text-fg"
            >
              {ref.service}
            </Link>
          </span>
        }
        title={<span className="font-mono">r{release.number}</span>}
        after={<span className="text-xs text-faint">{ref.environment}</span>}
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
      <Page width="max-w-[1040px]" gap="gap-[26px]">
        <section aria-labelledby="release" className="flex flex-col gap-3.5">
          <h2 id="release" className="text-xl leading-tight font-semibold">
            <Title release={release} />
          </h2>
          {failed && (
            <p className="text-md leading-normal">
              {serving && serving !== release.number ? (
                <>
                  <span className="text-ok">●</span>{" "}
                  <b className="font-semibold">r{serving} is still serving.</b>{" "}
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
          <dl className="props max-w-[960px] grid-cols-[140px_minmax(0,1fr)]">
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
            {previous && (
              <Prop label={`Changed since r${previous.number}`}>
                <Changes from={previous} to={release} />
              </Prop>
            )}
          </dl>
          <div className="mt-1 flex flex-wrap items-start gap-2.5">
            <Act
              primary
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
                image={service.image}
                disabled={
                  !!view.deploying ||
                  !!blocked ||
                  deploy.isPending ||
                  gitDeploy.isPending
                }
                title={blocked}
                onImage={() => deploy.mutate(ref, { onSuccess: toService })}
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
                disabled={!!view.deploying || !!blocked}
                title={
                  blocked ??
                  (view.deploying
                    ? `r${view.deploying.number} is deploying`
                    : undefined)
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

        <section aria-labelledby="steps" className="flex flex-col gap-2">
          <SectionHead id="steps">
            {failed ? "What failed" : "Steps"}
          </SectionHead>
          <StepTable steps={deploySteps(release, serving, service.stateful)} />
          {failed && <NextStep release={release} />}
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
  onImage: () => void;
  onCommit: (sha: string) => void;
  onOpen: () => void;
}) {
  const ref = servicePath(Route.useParams());
  const flags = `-s ${ref.service} -e ${ref.environment}`;
  const { commit } = release;
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
        cli={`dock deploy ${flags}`}
        disabled={disabled}
        title={title}
        onClick={onImage}
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

/**
 * The likely fix for a failed release, under its steps: a refused port
 * opens the port setting, a failing health check its path.
 */
function NextStep({ release }: { release: Release }) {
  const org = useOrg();
  const ref = servicePath(Route.useParams());
  const port = refusedPort(release);
  const path = release.config.health_check_path;
  const build = (edit: "port" | "health_check_path", label: string) => (
    <Link
      to="/$org/$project/$environment/$service/build"
      params={{ org, ...ref }}
      search={{ edit }}
      className="btn sm"
    >
      {label}
    </Link>
  );
  if (port !== undefined) {
    return (
      <p className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <span>
          {ref.service} didn't answer on{" "}
          <span className="font-mono">:{port}</span>. If the app listens on
          another port, change it in Build &amp; run, then deploy again.
        </span>
        {build("port", `Change port (now ${release.config.port})`)}
      </p>
    );
  }
  if (path && /^health check/i.test(release.error ?? "")) {
    return (
      <p className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <span>
          <span className="font-mono">GET {path}</span> didn't answer with a
          success. Check the app's logs below, or the path in Build &amp; run.
        </span>
        {build("health_check_path", "Change health check path")}
      </p>
    );
  }
  return null;
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
            <LogRow key={row.key} line={row} />
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
