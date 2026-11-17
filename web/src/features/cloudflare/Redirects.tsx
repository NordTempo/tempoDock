import { lazy, Suspense, useState } from "react";
import { ago } from "../../ui/format";
import {
  Act,
  Button,
  Confirm,
  ErrorText,
  Page,
  Panel,
  StateText,
} from "../../ui/kit";
import type { State } from "../../ui/status";
import {
  type CloudflareScope,
  type Redirect,
  useCloudflare,
  useRedirects,
  useRemoveRedirect,
  useRetryRedirect,
} from "./data";

// The Cloudflare page's Redirects tab (ADR 0068, mock C): the level's
// redirects, hostnames in its own zones that Cloudflare sends to a URL.
// A filter and a zone pick narrow the list; Add redirect and Edit open
// RedirectDialog.

const RedirectDialog = lazy(() => import("./RedirectDialog"));

/** Rows shown at once; the filter narrows a longer list. */
const shownMax = 200;

const states: Record<Redirect["status"], State> = {
  pending: { tone: "prog", glyph: "◐", label: "pending" },
  live: { tone: "ok", glyph: "●", label: "live" },
  error: { tone: "fail", glyph: "✕", label: "failed" },
  deleting: { tone: "mut", glyph: "◐", label: "removing" },
};

/** The CLI flag of the scope. */
export const scopeFlag = (scope: CloudflareScope) =>
  scope === "platform" ? " --platform" : "";

export default function Redirects({
  scope,
  adding,
  onAdding,
}: {
  scope: CloudflareScope;
  /** The page's Add redirect was pressed. */
  adding: boolean;
  onAdding: (open: boolean) => void;
}) {
  const redirects = useRedirects(scope);
  const connections = useCloudflare(scope);
  // A zone on several of the level's connections is listed once.
  const zones = [...new Set(connections.flatMap((c) => c.zones ?? []))].sort();
  const [filter, setFilter] = useState("");
  const [zone, setZone] = useState("");
  const [editing, setEditing] = useState<Redirect>();
  const needle = filter.trim().toLowerCase();
  const matching = redirects.filter(
    (r) =>
      (!zone || r.zone === zone) &&
      (!needle ||
        r.hostname.includes(needle) ||
        r.target.toLowerCase().includes(needle)),
  );
  const shown = matching.slice(0, shownMax);
  return (
    <Page gap="gap-3">
      {redirects.length > 0 && (
        <div className="flex flex-wrap gap-2">
          <input
            aria-label="Filter redirects"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder="Filter hostnames or targets"
            className="field w-[280px] max-w-full py-[5px]"
          />
          {zones.length > 1 && (
            <select
              aria-label="Zone"
              value={zone}
              onChange={(e) => setZone(e.target.value)}
              className="field py-[5px] font-mono"
            >
              <option value="">All zones</option>
              {zones.map((z) => (
                <option key={z} value={z}>
                  {z}
                </option>
              ))}
            </select>
          )}
        </div>
      )}
      {redirects.length === 0 ? (
        <p className="py-2 text-mut">
          {zones.length === 0
            ? scope === "platform"
              ? "No redirects. The platform's connections reach no zone yet; add a connection first."
              : "No redirects. Redirects use this organization's own zones; add a Cloudflare connection first. Zones the platform shares keep their redirects on the platform."
            : "No redirects. A redirect sends a hostname in your zones, like instagram." +
              zones[0] +
              ", to any URL. Add one with Add redirect."}
        </p>
      ) : (
        <Panel>
          <table>
            <thead>
              <tr>
                <th>Hostname</th>
                <th>Target</th>
                <th>Zone</th>
                <th>Type</th>
                <th>Status</th>
                <th>Changed</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {shown.map((r) => (
                <Row
                  key={r.hostname}
                  scope={scope}
                  redirect={r}
                  onEdit={() => setEditing(r)}
                />
              ))}
            </tbody>
          </table>
        </Panel>
      )}
      {redirects.length > 0 && matching.length === 0 && (
        <p className="text-mut">No redirect matches.</p>
      )}
      {matching.length > shown.length && (
        <p className="text-mut">
          Showing {shown.length} of {matching.length}; filter to narrow.
        </p>
      )}
      {(adding || editing) && (
        <Suspense>
          <RedirectDialog
            scope={scope}
            zones={zones}
            redirect={editing}
            onClose={() => {
              onAdding(false);
              setEditing(undefined);
            }}
          />
        </Suspense>
      )}
    </Page>
  );
}

/** One redirect: where it goes, how far Dock is, Edit, Retry and Remove. */
function Row({
  scope,
  redirect: r,
  onEdit,
}: {
  scope: CloudflareScope;
  redirect: Redirect;
  onEdit: () => void;
}) {
  const remove = useRemoveRedirect(scope);
  const retry = useRetryRedirect(scope);
  const [confirming, setConfirming] = useState(false);
  const permanent = r.status_code === 301 || r.status_code === 308;
  const gone = r.status === "deleting";
  return (
    <tr>
      <td className="py-2 font-mono">{r.hostname}</td>
      <td className="max-w-[420px] truncate py-2 font-mono text-sec">
        {r.target}
        {r.keep_path && (
          <span className="text-faint" title="Keeps the path and query">
            /…
          </span>
        )}
      </td>
      <td className="py-2 font-mono text-mut">{r.zone || r.connection}</td>
      <td
        className="py-2 text-mut"
        title={
          permanent
            ? "Permanent: browsers keep it"
            : "Temporary: browsers ask again"
        }
      >
        {r.status_code}
      </td>
      <td className="py-2 whitespace-normal">
        <StateText state={states[r.status]} />
        {r.reason && <span className="text-mut"> {r.reason}</span>}
        {r.note && <div className="text-mut">{r.note}</div>}
      </td>
      <td className="py-2 text-mut">
        {ago(r.updated_at)} · {r.updated_by}
      </td>
      <td className="w-full py-1.5 text-right">
        <span className="inline-flex items-center gap-3">
          <ErrorText error={retry.error} />
          {r.status === "error" && (
            <Act
              small
              cli={`dock cloudflare redirects retry ${r.hostname}${scopeFlag(scope)}`}
              disabled={retry.isPending}
              onClick={() => retry.mutate(r.hostname)}
            >
              Retry
            </Act>
          )}
          <Button
            small
            disabled={gone}
            reason="it is being removed"
            onClick={onEdit}
          >
            Edit
          </Button>
          <Button
            danger
            small
            disabled={gone}
            reason="it is being removed"
            onClick={() => setConfirming(true)}
          >
            Remove
          </Button>
          {confirming && (
            <Confirm
              danger
              action={`Remove ${r.hostname}`}
              cli={`dock cloudflare redirects remove ${r.hostname}${scopeFlag(scope)}`}
              pending={remove.isPending}
              error={remove.error}
              onConfirm={() =>
                remove.mutate(r.hostname, {
                  onSuccess: () => setConfirming(false),
                })
              }
              onCancel={() => {
                remove.reset();
                setConfirming(false);
              }}
            >
              Dock deletes its rule and record in Cloudflare, and{" "}
              <b>{r.hostname}</b> stops redirecting to {r.target}.
            </Confirm>
          )}
        </span>
      </td>
    </tr>
  );
}
