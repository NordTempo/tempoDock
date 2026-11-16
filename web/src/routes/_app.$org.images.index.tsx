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
import { useOrg } from "../shell/location";
import { day, since } from "../ui/format";
import { Icon } from "../ui/icons";
import { Button, Page, PageBar, Panel } from "../ui/kit";

// The org's images in use (ADR 0074, mock I1): one row per image however
// many services run it, worst first as the API sorts them, with counts by
// severity. /acme/images?everywhere=1&show=vulnerable
type Show = "vulnerable" | "unscanned";
type Search = { everywhere?: true; show?: Show };

export const Route = createFileRoute("/_app/$org/images/")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    everywhere: s.everywhere === true || s.everywhere === "1" || undefined,
    show:
      s.show === "vulnerable" || s.show === "unscanned" ? s.show : undefined,
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

function Images() {
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const { images: all, database_built } = useImages(!search.everywhere);
  const images = all ?? [];
  const [shown, setShown] = useState(CAP);
  const counts = {
    all: images.length,
    vulnerable: images.filter(vulnerable).length,
    unscanned: images.filter((i) => !scanned(i)).length,
  };
  const rows = images.filter((i) =>
    search.show === "vulnerable"
      ? vulnerable(i)
      : search.show === "unscanned"
        ? !scanned(i)
        : true,
  );
  const set = (next: Search) =>
    navigate({ search: { ...search, ...next }, replace: true });
  return (
    <>
      <PageBar
        title="Images"
        actions={
          <Link
            to="/$org/images/ignores"
            params={{ org: useOrg() }}
            className="btn no-underline"
            title="dock vulns ignores"
          >
            Ignores
          </Link>
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
                  <th className="text-right">Fixable</th>
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
        {database_built && (
          <p className="text-xs text-mut">
            Vulnerability database from {day(database_built)}.
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
          <Count n={scan.fixable} />
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
