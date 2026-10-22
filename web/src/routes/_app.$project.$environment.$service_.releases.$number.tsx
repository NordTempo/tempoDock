import {
  createFileRoute,
  Link,
  notFound,
  useNavigate,
} from "@tanstack/react-router";
import type { ReactNode } from "react";
import { useServiceLogs } from "../data/live";
import { useDeploy } from "../data/mutations";
import {
  findService,
  queries,
  servicePath,
  useDeployBlock,
  useRelease,
  useReleases,
  useService,
} from "../data/queries";
import type { LogLine, Release } from "../data/types";
import { LogRow } from "../features/service/LogsPane";
import { imageLabel } from "../features/service/releases";
import { deploySteps, StepTable } from "../features/service/Steps";
import { useShortcuts } from "../shell/keys";
import { ago, plural } from "../ui/format";
import {
  Act,
  Actor,
  actorKind,
  ErrorText,
  Glyph,
  Page,
  SectionHead,
} from "../ui/kit";
import { failurePhrase, inFlight, releaseWhat } from "../ui/status";

// One release: why it failed (or how far it got), its steps, its last lines.
// Outside the service layout, like a document of its own: /shop/staging/web/releases/61.
export const Route = createFileRoute(
  "/_app/$project/$environment/$service_/releases/$number",
)({
  loader: async ({ context: { queryClient }, params }) => {
    const number = Number(params.number);
    if (!Number.isInteger(number) || number < 1) throw notFound();
    const overview = await queryClient.ensureQueryData(
      queries.overview(params.project),
    );
    if (!findService(overview, params)) throw notFound();
    const ref = servicePath(params);
    await Promise.all([
      queryClient.ensureQueryData(queries.release(ref, number)),
      queryClient.ensureQueryData(queries.releases(ref)),
    ]);
  },
  component: ReleasePage,
});

function ReleasePage() {
  const params = Route.useParams();
  const ref = servicePath(params);
  const view = useService(ref);
  const release = useRelease(ref, Number(params.number));
  const releases = useReleases(ref);
  const deploy = useDeploy();
  const blocked = useDeployBlock(ref);
  const navigate = useNavigate();
  const { service } = view;
  const serving = service.active_release ?? null;
  const failed = release.status === "failed";
  const r = `r${release.number}`;
  // What it replaced (or would have): the release before it that served.
  const previous = releases.find(
    (x) =>
      x.number < release.number &&
      (x.status === "active" || x.status === "superseded"),
  );

  const buildLog = { release: release.number, build: true };
  useShortcuts([
    {
      key: "l",
      label: "build log",
      run: () =>
        navigate({
          to: "/$project/$environment/$service/logs",
          params: ref,
          search: buildLog,
        }),
    },
  ]);

  return (
    <Page width="max-w-[1040px]" gap="gap-[26px]">
      <section aria-labelledby="release" className="flex flex-col gap-3.5 pt-1">
        <h1 id="release" className="text-[24px] leading-tight font-semibold">
          <Title release={release} />
        </h1>
        {failed && (
          <p className="text-lg leading-normal">
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
        <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-[18px] gap-y-1.5">
          <Fact label="Service">
            <span className="font-mono">{ref.service}</span> in{" "}
            {ref.environment}
          </Fact>
          <Fact label="Deployed by">
            <Actor
              name={release.created_by}
              kind={actorKind(release.created_by)}
            />{" "}
            · {ago(release.created_at)}
          </Fact>
          <Fact label="What">
            {releaseWhat(release)}{" "}
            <span className="font-mono">{release.image_ref}</span>
          </Fact>
          {previous && (
            <Fact label={`Changed since r${previous.number}`}>
              <Changes from={previous} to={release} />
            </Fact>
          )}
        </dl>
        <div className="mt-1 flex flex-wrap items-start gap-2.5">
          <Act
            primary
            cli={`dock logs --build -r ${release.number} -s ${ref.service} -e ${ref.environment}`}
            onClick={() =>
              navigate({
                to: "/$project/$environment/$service/logs",
                params: ref,
                search: buildLog,
              })
            }
          >
            Open build log <kbd>L</kbd>
          </Act>
          {failed && service.image && (
            <Act
              cli={`dock deploy -s ${ref.service} -e ${ref.environment}`}
              disabled={!!view.deploying || !!blocked || deploy.isPending}
              title={blocked}
              onClick={() =>
                deploy.mutate(ref, {
                  onSuccess: () =>
                    navigate({
                      to: "/$project/$environment/$service",
                      params: ref,
                    }),
                })
              }
            >
              Deploy {imageLabel({ image_ref: service.image, image_id: "" })}{" "}
              again
            </Act>
          )}
          <Act
            cli={`dock env set -s ${ref.service} -e ${ref.environment}`}
            onClick={() =>
              navigate({
                to: "/$project/$environment",
                params: ref,
                search: { service: ref.service },
              })
            }
          >
            Variables of {ref.service}
          </Act>
          <span className="self-center">
            {failed && service.image && blocked && (
              <span className="text-fail">Disabled: {blocked}. </span>
            )}
            <ErrorText error={deploy.error} />
          </span>
        </div>
      </section>

      <section aria-labelledby="steps" className="flex flex-col gap-2">
        <SectionHead id="steps">{failed ? "What failed" : "Steps"}</SectionHead>
        <StepTable steps={deploySteps(release, serving, service.stateful)} />
      </section>

      <LastLines release={release} />
    </Page>
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

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-mut">{label}</dt>
      <dd>{children}</dd>
    </>
  );
}

/** Image and variable names that differ between two releases. */
function Changes({ from, to }: { from: Release; to: Release }) {
  const before = new Set(from.config.variable_keys ?? []);
  const after = new Set(to.config.variable_keys ?? []);
  const added = [...after].filter((k) => !before.has(k));
  const removed = [...before].filter((k) => !after.has(k));
  const parts: ReactNode[] = [];
  if (from.image_id !== to.image_id) {
    parts.push(
      <span key="image">
        image <span className="font-mono text-mut">{imageLabel(from)}</span> →{" "}
        <span className="font-mono">{imageLabel(to)}</span>
        {imageLabel(from) === imageLabel(to) && " (new digest)"}
      </span>,
    );
  }
  if (added.length)
    parts.push(
      <span key="add">
        added <span className="font-mono">{added.join(", ")}</span>
      </span>,
    );
  if (removed.length)
    parts.push(
      <span key="rm">
        removed <span className="font-mono">{removed.join(", ")}</span>
      </span>,
    );
  if (from.config.port !== to.config.port)
    parts.push(
      <span key="port">
        port {from.config.port} → {to.config.port}
      </span>,
    );
  if (from.config.health_check_path !== to.config.health_check_path) {
    parts.push(
      <span key="hc">
        health check {from.config.health_check_path || "TCP"} →{" "}
        {to.config.health_check_path || "TCP"}
      </span>,
    );
  }
  if (parts.length === 0)
    return <span className="text-mut">same image and variable names</span>;
  return parts.flatMap((part, i) => (i ? [" · ", part] : [part]));
}

/** The release's last container lines; for a failed one, why it stopped. */
function LastLines({ release }: { release: Release }) {
  const params = Route.useParams();
  const ref = servicePath(params);
  const logs = useServiceLogs(ref, release.number);
  const failed = release.status === "failed";
  // Oldest first, repeats folded, then the last screenful.
  const rows = collapseRepeats(logs.lines.slice(0, 200).reverse()).slice(-14);
  return (
    <section aria-labelledby="lines" className="flex flex-col gap-2">
      <SectionHead
        id="lines"
        right={
          <Link
            to="/$project/$environment/$service/logs"
            params={ref}
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
