import { createFileRoute, Link } from "@tanstack/react-router";
import { useState } from "react";
import {
  type ImageListItem,
  imageQueries,
  useImages,
} from "../features/images/data";
import {
  below,
  hasFindings,
  Kev,
  notScanned,
  rescan,
  total,
  UsedBy,
} from "../features/images/parts";
import {
  shortImage,
  UpdateMany,
  updateTargets,
} from "../features/images/UpdateMany";
import { useOrg } from "../shell/location";
import { ago, day, since } from "../ui/format";
import { Icon } from "../ui/icons";
import { Button, Page, PageBar, Panel } from "../ui/kit";

// The org's images in use (ADR 0074, mock I1): one row per image however
// many services run it, worst first as the API sorts them, with counts by
// severity, and (image updates, mock A) what a newer image of the tag it
// follows offers: Update, or Update all of them.
// /acme/images?everywhere=1&show=vulnerable
type Show = "vulnerable" | "updates" | "unscanned";
type Search = { everywhere?: true; show?: Show };

export const Route = createFileRoute("/_app/$org/images/")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    everywhere: s.everywhere === true || s.everywhere === "1" || undefined,
    show:
      s.show === "vulnerable" || s.show === "updates" || s.show === "unscanned"
        ? s.show
        : undefined,
  }),
  loaderDeps: ({ search }) => ({ everywhere: !!search.everywhere }),
  loader: ({ context: { queryClient }, deps }) =>
    queryClient.ensureQueryData(imageQueries.list(!deps.everywhere)),
  component: Images,
});

/** Rows drawn at first; images in use are few, but a list is capped. */
const CAP = 200;

const scanned = (i: ImageListItem) => hasFindings(i.scan);
const vulnerable = (i: ImageListItem) =>
  hasFindings(i.scan) && total(i.scan) > 0;
/** The tag it follows, or its build's base, has a newer image. */
const behind = (i: ImageListItem) => !!i.scan?.base?.moved;

function Images() {
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const { images: all, database_built } = useImages(!search.everywhere);
  const images = all ?? [];
  const [shown, setShown] = useState(CAP);
  const counts = {
    all: images.length,
    vulnerable: images.filter(vulnerable).length,
    updates: images.filter(behind).length,
    unscanned: images.filter((i) => !scanned(i)).length,
  };
  const rows = images.filter((i) =>
    search.show === "vulnerable"
      ? vulnerable(i)
      : search.show === "updates"
        ? behind(i)
        : search.show === "unscanned"
          ? !scanned(i)
          : true,
  );
  const updates = rows.flatMap(updateTargets);
  // The daily check writes every tag's answer at once: the newest says
  // when it last ran.
  const checked = images
    .map((i) => i.scan?.base?.checked_at)
    .filter((t): t is string => !!t)
    .sort()
    .at(-1);
  const set = (next: Search) =>
    navigate({ search: { ...search, ...next }, replace: true });
  return (
    <>
      <PageBar
        title="Images"
        actions={
          <>
            {updates.length > 0 && (
              <UpdateMany
                targets={updates}
                label={`Update all ${updates.length}`}
              />
            )}
            <Link
              to="/$org/images/ignores"
              params={{ org: useOrg() }}
              className="btn no-underline"
              title="dock vulns ignores"
            >
              Ignores
            </Link>
          </>
        }
      />
      <Page gap="gap-3">
        {/* Two groups, where and which, split by the divider the service
            tab strip uses. */}
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
          <span className="seg">
            <button
              type="button"
              aria-pressed={!search.everywhere}
              onClick={() => set({ everywhere: undefined })}
            >
              Production
            </button>
            <button
              type="button"
              aria-pressed={!!search.everywhere}
              onClick={() => set({ everywhere: true })}
            >
              Everywhere
            </button>
          </span>
          <span aria-hidden className="h-[18px] w-px flex-none bg-edge" />
          <span className="seg">
            <button
              type="button"
              aria-pressed={!search.show}
              onClick={() => set({ show: undefined })}
            >
              All {counts.all}
            </button>
            <button
              type="button"
              aria-pressed={search.show === "vulnerable"}
              onClick={() => set({ show: "vulnerable" })}
            >
              With vulnerabilities {counts.vulnerable}
            </button>
            <button
              type="button"
              aria-pressed={search.show === "updates"}
              onClick={() => set({ show: "updates" })}
            >
              Updates {counts.updates}
            </button>
            <button
              type="button"
              aria-pressed={search.show === "unscanned"}
              onClick={() => set({ show: "unscanned" })}
            >
              Not scanned {counts.unscanned}
            </button>
          </span>
        </div>
        {rows.length === 0 ? (
          <p className="border-t border-line pt-3 text-mut">
            {images.length === 0
              ? search.everywhere
                ? "No live releases yet."
                : "No live releases in production yet."
              : "None."}
          </p>
        ) : (
          <Panel>
            <table className="tabular-nums">
              <thead>
                <tr>
                  <th>Image</th>
                  <th>Used by</th>
                  <th>Base</th>
                  <th className="text-right">Critical</th>
                  <th className="text-right">High</th>
                  <th className="text-right">Other</th>
                  <th>Update</th>
                  <th>Scanned</th>
                  <th>
                    <span className="sr-only">Open</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.slice(0, shown).map((item, i) => (
                  <Row
                    key={item.scan?.id ?? `${item.image}:${i}`}
                    item={item}
                  />
                ))}
              </tbody>
            </table>
          </Panel>
        )}
        {rows.length > shown && (
          <div>
            <Button small onClick={() => setShown(rows.length)}>
              Show all {rows.length}
            </Button>
          </div>
        )}
        {(database_built || checked) && (
          <p className="text-xs text-mut">
            {database_built &&
              `Vulnerability database from ${day(database_built)}.`}
            {checked && ` Tags checked ${ago(checked)}.`}
          </p>
        )}
      </Page>
    </>
  );
}

/** A count, dim at zero. */
function Count({
  n,
  tone = "",
  kev,
}: {
  n: number;
  tone?: string;
  kev?: boolean;
}) {
  return (
    <td className={`text-right font-mono ${n === 0 ? "text-faint" : tone}`}>
      {n}
      {kev && <Kev />}
    </td>
  );
}

/** One image: its counts once scanned, else why not, dimmed. */
function Row({ item }: { item: ImageListItem }) {
  const org = useOrg();
  const { scan } = item;
  // A pinned digest shows its first 7 characters, as elsewhere; the full
  // reference is the title.
  const [ref = "", digest] = item.image.split("@sha256:");
  const name = (
    <span className="font-mono break-all" title={item.image}>
      {ref}
      {digest && <span className="text-mut">@{digest.slice(0, 7)}</span>}
    </span>
  );
  return (
    <tr className={scan ? "row-link" : ""}>
      <td className="max-w-[360px] whitespace-normal">
        {scan ? (
          <Link
            to="/$org/images/$id"
            params={{ org, id: String(scan.id) }}
            className="row-main hover:underline"
          >
            {name}
          </Link>
        ) : (
          name
        )}
      </td>
      <td>
        <UsedBy uses={item.used_by ?? []} />
      </td>
      <td className="font-mono text-sec">
        {scan?.distro || <span className="text-faint">·</span>}
      </td>
      {hasFindings(scan) ? (
        <>
          <Count n={scan.critical} tone="text-fail" kev={scan.kev > 0} />
          <Count n={scan.high} tone="text-warn" />
          <Count n={below(scan)} />
          <td>
            <UpdateCell item={item} />
          </td>
          <td className="text-mut" title={scan.scanned_at}>
            {since(scan.scanned_at)}
            {rescan(scan) && (
              <span className="text-faint"> · {rescan(scan)}</span>
            )}
          </td>
        </>
      ) : (
        <td colSpan={5} className="text-faint">
          {scan?.state === "failed" || scan?.state === "unsupported"
            ? `not scanned: ${notScanned(scan)}`
            : notScanned(scan)}
        </td>
      )}
      <td className="row-go" aria-hidden>
        {scan && <Icon name="chevronRight" size={14} />}
      </td>
    </tr>
  );
}

/**
 * What a newer image offers: Update for an image deployed by tag (and
 * whether its services update on their own), a note for one Dock built,
 * whose next deploy builds on the newer base.
 */
function UpdateCell({ item }: { item: ImageListItem }) {
  const base = item.scan?.base;
  if (!base?.moved)
    return <span className="text-faint">{base ? "up to date" : "·"}</span>;
  if (!base.own)
    return (
      <span className="text-mut" title={`${base.image} has a newer image`}>
        newer base · redeploy
      </span>
    );
  const targets = updateTargets(item);
  const auto = (item.used_by ?? []).some((u) => u.live && u.auto_update);
  return (
    <span
      className="inline-flex items-center gap-2"
      title={base.latest && shortImage(`${base.image}@${base.latest}`)}
    >
      <UpdateMany small targets={targets} label="Update" />
      {auto && <span className="text-mut">auto</span>}
    </span>
  );
}
