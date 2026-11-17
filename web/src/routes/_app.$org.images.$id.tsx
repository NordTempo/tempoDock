import { createFileRoute, Link, notFound } from "@tanstack/react-router";
import { lazy, Suspense, useState } from "react";
import { withOrg } from "../api/client";
import { canDeploy, useSession } from "../data/queries";
import type { ServiceRef } from "../data/types";
import { openDeploy } from "../features/deploy/start";
import {
  type Finding,
  type FixGroup,
  type ImageDetail,
  type ImageUse,
  imageQueries,
  useImage,
  useScanService,
} from "../features/images/data";
import { IgnoresTable } from "../features/images/IgnoresTable";
import { mayIgnore } from "../features/images/ignores";
import {
  below,
  edgeOf,
  fixTitle,
  hasFindings,
  Kev,
  notScanned,
  rescan,
  severityTone,
  total,
  WorstCounts,
} from "../features/images/parts";
import { useOrg } from "../shell/location";
import { ago, day, plural, since } from "../ui/format";
import { Icon } from "../ui/icons";
import {
  Act,
  Button,
  ErrorText,
  Menu,
  menuItem,
  Page,
  PageBar,
  Panel,
  Prop,
  SectionHead,
} from "../ui/kit";

// One image (ADR 0074): where it runs, its scan, its findings grouped by
// what fixes them, all of them behind a switch (each can be ignored,
// mock I1), and the ignores that hide some (L2). /acme/images/12?view=all
type View = "all" | "ignored";
type Search = { view?: View };

const IgnoreDialog = lazy(() => import("../features/images/IgnoreDialog"));

export const Route = createFileRoute("/_app/$org/images/$id")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    view: s.view === "all" || s.view === "ignored" ? s.view : undefined,
  }),
  loader: ({ context: { queryClient }, params }) => {
    const id = Number(params.id);
    if (!Number.isInteger(id) || id < 1)
      throw notFound({ routeId: "/_app/$org" });
    return queryClient.ensureQueryData(imageQueries.image(id));
  },
  component: ImagePage,
});

const refOf = (u: ImageUse): ServiceRef => ({
  project: u.project,
  environment: u.environment,
  service: u.service,
});

function ImagePage() {
  const org = useOrg();
  const detail = useImage(Number(Route.useParams().id));
  const { scan } = detail;
  const uses = detail.used_by ?? [];
  // The service the buttons act through: the first that runs it live.
  const use = uses.find((u) => u.live) ?? uses[0];
  return (
    <>
      <PageBar
        parent={
          <Link
            to="/$org/images"
            params={{ org }}
            className="-my-[3px] inline-flex min-h-[24px] items-center hover:text-fg"
          >
            Images
          </Link>
        }
        title={<span className="font-mono break-all">{scan.image}</span>}
        actions={use && <Actions detail={detail} use={use} />}
      />
      <Page>
        <dl className="props grid-cols-[140px_minmax(0,1fr)]">
          {scan.digest && (
            <Prop label="Digest">
              <span className="font-mono break-all">{scan.digest}</span>
            </Prop>
          )}
          <Prop label="Used by">
            {uses.length === 0 ? (
              <span className="text-faint">no live release</span>
            ) : (
              <span className="flex flex-col">
                {uses.map((u) => (
                  <UseLine
                    key={`${u.project}/${u.environment}/${u.service}/${u.release}`}
                    use={u}
                  />
                ))}
              </span>
            )}
          </Prop>
          {(scan.distro || scan.base) && (
            <Prop label="Base">
              {scan.distro && <span className="font-mono">{scan.distro}</span>}
              {scan.packages > 0 && (
                <span className="text-mut">
                  {" "}
                  · {plural(scan.packages, "package")}
                </span>
              )}
              {scan.base && <BaseLine base={scan.base} lead={!!scan.distro} />}
            </Prop>
          )}
          <Prop label="Scanned">
            {hasFindings(scan) ? (
              <>
                <span title={scan.scanned_at}>{ago(scan.scanned_at)}</span>
                {scan.database_built && (
                  <span className="text-mut">
                    {" "}
                    · database of {day(scan.database_built)}
                  </span>
                )}
                {rescan(scan) && (
                  <span className="text-mut"> · {rescan(scan)}</span>
                )}
              </>
            ) : (
              <span className="text-mut">{notScanned(scan)}</span>
            )}
          </Prop>
        </dl>
        {hasFindings(scan) && <Vulnerabilities detail={detail} use={use} />}
      </Page>
    </>
  );
}

/**
 * The image a Dockerfile build was built on: "· built on node:22-alpine",
 * and, once the daily check saw its tag move, that a rebuild picks up
 * the newer one.
 */
function BaseLine({
  base,
  lead,
}: {
  base: NonNullable<ImageDetail["scan"]["base"]>;
  lead: boolean;
}) {
  return (
    <>
      <span className="text-mut">{lead ? " · built on " : "built on "}</span>
      <span className="font-mono" title={base.digest}>
        {base.image}
      </span>
      {base.moved && (
        <span className="text-warn" title={base.checked_at}>
          {" "}
          · the tag has a newer image: a rebuild picks it up
        </span>
      )}
    </>
  );
}

/** "shop / production / web, release 42 (live)", linking to the release. */
function UseLine({ use }: { use: ImageUse }) {
  const org = useOrg();
  return (
    <span>
      {use.project} / {use.environment} / {use.service},{" "}
      <Link
        to="/$org/$project/$environment/$service/history/$number"
        params={{ org, ...refOf(use), number: String(use.release) }}
        className="text-info hover:underline"
      >
        release {use.release}
      </Link>
      {use.live && <span className="text-mut"> (live)</span>}
    </span>
  );
}

/**
 * SBOM and Scan again, through the first service that runs the image
 * live: the SBOM once it was scanned, a rescan for deployers while none
 * is waiting or running.
 */
function Actions({ detail, use }: { detail: ImageDetail; use: ImageUse }) {
  const ref = refOf(use);
  const { scan } = detail;
  const mayScan = canDeploy(useSession(), ref);
  const scanAgain = useScanService();
  const flags = `${ref.service} -e ${ref.environment}${use.live ? "" : ` -r ${use.release}`}`;
  const path = (format: string) =>
    `/api/v1/projects/${encodeURIComponent(ref.project)}/environments/${encodeURIComponent(ref.environment)}/services/${encodeURIComponent(ref.service)}/sbom?release=${use.release}&format=${format}`;
  const busy =
    scanAgain.isPending || scan.state === "queued" || scan.state === "scanning";
  return (
    <>
      <ErrorText error={scanAgain.error} />
      {scan.scanned_at && (
        <Menu
          label="Download the SBOM"
          right
          button={
            <span className="btn">
              SBOM <Icon name="chevronDown" size={12} />
            </span>
          }
        >
          {(close) =>
            sbomFormats.map((f) => (
              <a
                key={f.format}
                href={withOrg(path(f.format))}
                download
                role="menuitem"
                className={menuItem}
                title={`dock sbom ${flags}${f.format === "syft-json" ? "" : ` --format ${f.format}`} > sbom.json`}
                onClick={close}
              >
                {f.label}
              </a>
            ))
          }
        </Menu>
      )}
      {mayScan && (
        <Act
          cli={`dock scan ${flags}`}
          disabled={busy}
          reason={
            scan.state === "queued"
              ? "Queued"
              : scan.state === "scanning"
                ? "Scanning now"
                : undefined
          }
          onClick={() =>
            scanAgain.mutate({
              ref,
              release: use.live ? undefined : use.release,
            })
          }
        >
          Scan again
        </Act>
      )}
    </>
  );
}

/** The SBOM's download formats: as stored, and the two standards. */
const sbomFormats = [
  { format: "syft-json", label: "Syft JSON" },
  { format: "cyclonedx-json", label: "CycloneDX 1.6" },
  { format: "spdx-json", label: "SPDX 2.3" },
] as const;

/** Lines drawn at first in a group, and rows in the table. */
const GROUP_CAP = 20;
const TABLE_CAP = 200;

/** The findings: grouped by what fixes them, or all of them in a table. */
function Vulnerabilities({
  detail,
  use,
}: {
  detail: ImageDetail;
  use: ImageUse | undefined;
}) {
  const view = Route.useSearch().view;
  const navigate = Route.useNavigate();
  const fixes = detail.fixes ?? [];
  const findings = (detail.findings ?? []).filter((f) => !f.ignored);
  const ignores = detail.ignores ?? [];
  const { scan } = detail;
  const versions = new Map(
    (detail.findings ?? []).map((f) => [f.package, f.version]),
  );
  return (
    <section aria-labelledby="vulns">
      <SectionHead
        id="vulns"
        right={
          (total(scan) > 0 || ignores.length > 0) && (
            <span className="seg">
              <button
                type="button"
                aria-pressed={!view}
                onClick={() => navigate({ search: {}, replace: true })}
              >
                By fix
              </button>
              <button
                type="button"
                aria-pressed={view === "all"}
                onClick={() =>
                  navigate({ search: { view: "all" }, replace: true })
                }
              >
                All {total(scan)}
              </button>
              {ignores.length > 0 && (
                <button
                  type="button"
                  aria-pressed={view === "ignored"}
                  onClick={() =>
                    navigate({ search: { view: "ignored" }, replace: true })
                  }
                >
                  Ignored {ignores.length}
                </button>
              )}
            </span>
          )
        }
      >
        Vulnerabilities
      </SectionHead>
      {view === "ignored" && ignores.length > 0 ? (
        <IgnoresTable ignores={ignores} versions={versions} />
      ) : total(scan) === 0 ? (
        <p className="border-t border-line pt-3 text-mut">
          {ignores.length > 0 ? "None that isn't ignored." : "None found."}
        </p>
      ) : view === "all" ? (
        <FindingsTable findings={findings} uses={detail.used_by ?? []} />
      ) : (
        <div className="flex flex-col gap-3 border-t border-line pt-3">
          {fixes.map((g) => (
            <Group key={g.kind} group={g} use={use} built={scan.built} />
          ))}
        </div>
      )}
      {!!scan.truncated && (
        <p className="mt-2 text-xs text-mut">
          {plural(scan.truncated, "more finding")} not kept: only the first
          5,000 are.
        </p>
      )}
    </section>
  );
}

/**
 * One kind of fix as a ruled block: its title and counts, then one mono
 * line per package, "openssl 3.3.1-r0 → 3.3.2-r0 · CVE-2026-31337 KEV +2".
 */
function Group({
  group,
  use,
  built,
}: {
  group: FixGroup;
  use: ImageUse | undefined;
  built: boolean;
}) {
  const packages = group.packages ?? [];
  const [all, setAll] = useState(false);
  const session = useSession();
  const redeploy =
    group.kind === "rebuild" &&
    built &&
    use?.live &&
    canDeploy(session, refOf(use)) &&
    use;
  const rest = below(group);
  return (
    <div className={`border-l-2 py-1.5 pl-3.5 ${edgeOf(group)}`}>
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
        <h3 className="font-semibold">
          {fixTitle(group.kind, packages.length)}
        </h3>
        <span className="font-mono text-xs">
          <WorstCounts counts={group} />
          {rest > 0 && (
            <span className="text-faint">
              {group.critical + group.high > 0 && " "}
              {rest} medium or low
            </span>
          )}
          {group.kev > 0 && <Kev />}
        </span>
        {redeploy && (
          <Act
            small
            cli={`dock deploy -s ${redeploy.service} -e ${redeploy.environment}`}
            title="A new build picks up the base image's fixed packages"
            onClick={() => openDeploy(refOf(redeploy))}
          >
            Redeploy {redeploy.service}
          </Act>
        )}
      </div>
      <ul className="mt-1.5 flex flex-col gap-0.5 font-mono text-xs text-sec">
        {(all ? packages : packages.slice(0, GROUP_CAP)).map((p) => {
          const [first, ...more] = p.findings ?? [];
          return (
            <li key={`${p.package}@${p.version}`} className="break-words">
              <span className={severityTone(p.worst)}>{p.package}</span>{" "}
              {p.version}
              {p.fixed_in && <> → {p.fixed_in}</>}
              {first && <> · {first}</>}
              {p.kev && <Kev />}
              {more.length > 0 && (
                <span className="text-faint" title={more.join(", ")}>
                  {" "}
                  +{more.length}
                </span>
              )}
            </li>
          );
        })}
      </ul>
      {packages.length > GROUP_CAP && (
        <Button small className="mt-2" onClick={() => setAll(!all)}>
          {all ? "Show fewer" : `Show all ${packages.length}`}
        </Button>
      )}
    </div>
  );
}

/**
 * Every finding not ignored, worst first as the API sorts them; a page at
 * first. Each can be ignored by those who may somewhere the image runs.
 */
function FindingsTable({
  findings,
  uses,
}: {
  findings: Finding[];
  uses: ImageUse[];
}) {
  const [shown, setShown] = useState(TABLE_CAP);
  const [ignoring, setIgnoring] = useState<Finding>();
  const session = useSession();
  const canIgnore =
    mayIgnore(session, { scope: "org" }) ||
    uses.some((u) => mayIgnore(session, { scope: "service", ...u }));
  return (
    <>
      <Panel>
        <table className="tabular-nums">
          <thead>
            <tr>
              <th>Severity</th>
              <th>Vulnerability</th>
              <th>Package</th>
              <th>Installed</th>
              <th>Fixed in</th>
              <th>From</th>
              <th>Since</th>
              {canIgnore && (
                <th>
                  <span className="sr-only">Ignore</span>
                </th>
              )}
            </tr>
          </thead>
          <tbody>
            {findings.slice(0, shown).map((f) => (
              <tr key={`${f.id}:${f.package}:${f.version}`}>
                <td className={severityTone(f.severity)}>{f.severity}</td>
                <td className="font-mono">
                  {f.id}
                  {f.kev && <Kev />}
                </td>
                <td className="font-mono break-all">{f.package}</td>
                <td className="font-mono text-sec">{f.version}</td>
                <td className="font-mono">
                  {f.fixed_in || (
                    <span className="font-sans text-faint">none yet</span>
                  )}
                </td>
                <td className="text-mut" title={f.type}>
                  {f.system ? "system" : "app"}
                </td>
                <td className="text-mut" title={f.first_seen}>
                  {since(f.first_seen)}
                </td>
                {canIgnore && (
                  <td className="py-1 text-right">
                    <Button small onClick={() => setIgnoring(f)}>
                      Ignore
                    </Button>
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </Panel>
      {findings.length > shown && (
        <Button
          small
          className="mt-2"
          onClick={() => setShown(findings.length)}
        >
          Show all {findings.length}
        </Button>
      )}
      {ignoring && (
        <Suspense>
          <IgnoreDialog
            finding={ignoring}
            uses={uses}
            onClose={() => setIgnoring(undefined)}
          />
        </Suspense>
      )}
    </>
  );
}
