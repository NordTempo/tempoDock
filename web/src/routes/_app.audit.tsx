import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { queries, useAuditLog, useProjects } from "../data/queries";
import type { AuditEvent } from "../data/types";
import { AuditTable } from "../features/audit/AuditTable";
import { Button, Page, TitleRow } from "../ui/kit";

type AuditSearch = {
  project?: string;
  /** Action prefix, filtered by the API. */
  action?: string;
  /** The rest filter the loaded page of events. */
  kind?: "token" | "user";
  actor?: string;
  q?: string;
};

const str = (v: unknown) => (typeof v === "string" && v ? v : undefined);

export const Route = createFileRoute("/_app/audit")({
  validateSearch: (s: Record<string, unknown>): AuditSearch => ({
    project: str(s.project),
    action: str(s.action),
    kind: s.kind === "token" || s.kind === "user" ? s.kind : undefined,
    actor: str(s.actor),
    q: str(s.q),
  }),
  loaderDeps: ({ search }) => ({
    project: search.project,
    action: search.action,
  }),
  loader: ({ context, deps }) =>
    context.queryClient.ensureQueryData(queries.audit(deps)),
  component: Audit,
});

const actions = [
  ["release.", "deploys, restarts, rollbacks"],
  ["variables.", "variables"],
  ["changes.", "apply and discard"],
  ["domain.", "domains"],
  ["service.", "services"],
  ["token.", "tokens"],
  ["user.", "users and logins"],
  ["backup.", "backups"],
  ["data.", "data access"],
] as const;

function toCsv(events: AuditEvent[]) {
  const cell = (v: string | undefined) =>
    `"${(v ?? "").replaceAll('"', '""')}"`;
  const rows = events.map((e) =>
    [
      e.created_at,
      e.actor,
      e.actor_kind,
      e.action,
      e.project,
      e.environment,
      e.target,
      e.summary,
    ]
      .map(cell)
      .join(","),
  );
  return [
    "time,actor,actor_kind,action,project,environment,target,summary",
    ...rows,
  ].join("\n");
}

function download(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/csv" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

function Audit() {
  const search = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const projects = useProjects();
  const events = useAuditLog({
    project: search.project,
    action: search.action,
  });
  const set = (patch: Partial<AuditSearch>) =>
    navigate({ search: { ...search, ...patch }, replace: true });

  const q = search.q?.toLowerCase();
  const shown = events.filter(
    (e) =>
      (!search.kind || e.actor_kind === search.kind) &&
      (!search.actor || e.actor === search.actor) &&
      (!q || `${e.target} ${e.summary}`.toLowerCase().includes(q)),
  );
  const actors = [...new Set(events.map((e) => e.actor))].sort();

  const day = Date.now() - 86_400_000;
  const recent = shown.filter((e) => Date.parse(e.created_at) > day);
  const deploys = recent.filter((e) => e.action === "release.deploy");
  const byTokens = deploys.filter((e) => e.actor_kind === "token").length;
  const byPeople = recent.filter(
    (e) => e.actor_kind === "user" && !e.action.startsWith("user.login"),
  ).length;

  return (
    <Page gap="gap-4">
      <TitleRow
        title="Audit log"
        context="Everything people and tokens did, newest first."
        actions={
          <Button
            small
            onClick={() => download("dock-audit.csv", toCsv(shown))}
          >
            Export CSV
          </Button>
        }
      />
      <form
        aria-label="Filters"
        className="flex flex-wrap items-center gap-2.5"
        onSubmit={(e) => e.preventDefault()}
      >
        <fieldset aria-label="Actor type" className="seg">
          {(
            [
              [undefined, "Everyone"],
              ["token", "◇ Tokens"],
              ["user", "○ People"],
            ] as const
          ).map(([kind, label]) => (
            <button
              key={label}
              type="button"
              aria-pressed={search.kind === kind}
              onClick={() => set({ kind })}
            >
              {label}
            </button>
          ))}
        </fieldset>
        <select
          aria-label="Actor"
          value={search.actor ?? ""}
          onChange={(e) => set({ actor: e.target.value || undefined })}
          className="field py-[5px]"
        >
          <option value="">any actor</option>
          {actors.map((a) => (
            <option key={a}>{a}</option>
          ))}
        </select>
        <select
          aria-label="Action"
          value={search.action ?? ""}
          onChange={(e) => set({ action: e.target.value || undefined })}
          className="field py-[5px]"
        >
          <option value="">any action</option>
          {actions.map(([prefix, label]) => (
            <option key={prefix} value={prefix}>
              {label}
            </option>
          ))}
        </select>
        <select
          aria-label="Project"
          value={search.project ?? ""}
          onChange={(e) => set({ project: e.target.value || undefined })}
          className="field py-[5px]"
        >
          <option value="">all projects</option>
          {projects.map((p) => (
            <option key={p.id}>{p.name}</option>
          ))}
        </select>
        <input
          aria-label="Search"
          placeholder="search target"
          value={search.q ?? ""}
          onChange={(e) => set({ q: e.target.value || undefined })}
          className="field min-w-0 flex-[1_1_180px] py-[5px]"
        />
        <Link to="/audit" search={{}} className="text-mut">
          Clear
        </Link>
      </form>
      <p>
        Last 24h{search.project ? ` in ${search.project}` : ""}:{" "}
        <b className="font-semibold">
          {deploys.length} {deploys.length === 1 ? "deploy" : "deploys"}
          {byTokens > 0 && `, ${byTokens} by tokens`}
        </b>
        , {byPeople} {byPeople === 1 ? "change" : "changes"} by people.
      </p>
      <AuditTable events={shown} />
      <p className="text-xs text-mut">
        <span className="text-info">◇</span> token · ○ person · the newest 100
        events
        {search.action ? " of this kind" : ""}
      </p>
    </Page>
  );
}
