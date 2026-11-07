import { useQuery } from "@tanstack/react-query";
import { queries, useAuditLog } from "../../data/queries";
import type { AuditEvent } from "../../data/types";
import { Act, Button, Page, PageBar } from "../../ui/kit";
import { AuditTable } from "./AuditTable";
import { type AuditSearch, MAX } from "./search";

/** The API's default page of events. */
const PAGE = 100;

/** The action filter's groups: an API prefix and what it covers. */
const actions = [
  ["release.", "deploys, restarts, rollbacks"],
  ["variables.", "variables"],
  ["changes.", "apply and discard"],
  ["service.", "services"],
  ["domain.", "domains"],
  ["environment.", "environments and locks"],
  ["request.", "access requests"],
  ["project.", "projects"],
  ["job.", "jobs"],
  ["preview.", "previews"],
  ["backup.", "backups"],
  ["snapshot.", "snapshots"],
  ["data.", "data access"],
  ["token.", "tokens"],
  ["org.", "organization and members"],
  ["user.", "users and sign-ins"],
] as const;

/** The platform log's own groups (operators, in the console). */
const platformActions = [
  ["server.", "servers"],
  ["platform.", "Dock updates and address"],
  ["registry.", "registry"],
  ["alert.", "alerts"],
] as const;

/** Every filter control is this tall, so the row lines up. */
const control = "h-[28px]";

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

/**
 * The audit log, called Activity everywhere, with its filters: the
 * organization's (/audit, search.platform unset) or the platform's (the
 * console's /console/audit sets it; ADR 0062: the console shows platform
 * events only, an org's own activity is its members'). The route owns
 * the URL; onSearch replaces its search params.
 */
export function AuditPage({
  search,
  onSearch,
}: {
  search: AuditSearch;
  onSearch: (search: AuditSearch) => void;
}) {
  // The platform's log (the console) has no organization, so no projects.
  const projects = (
    useQuery({ ...queries.projects(), enabled: !search.platform }).data ?? []
  ).map((p) => p.name);
  // An archived project's link lands here too; keep it selectable.
  if (search.project && !projects.includes(search.project)) {
    projects.push(search.project);
  }
  const environments =
    useQuery({
      ...queries.overview(search.project ?? ""),
      enabled: !!search.project,
      select: (o) => o.environments.map((e) => e.environment.name),
    }).data ?? [];
  const events = useAuditLog({
    platform: search.platform,
    project: search.project,
    environment: search.environment,
    action: search.action,
    limit: search.limit,
  });
  const set = (patch: Partial<AuditSearch>) =>
    onSearch({ ...search, ...patch });
  const cli = [
    "dock audit",
    search.platform && "--platform",
    search.project && `-p ${search.project}`,
    search.environment && `-e ${search.environment}`,
    search.action && `--action ${search.action}`,
    `-n ${MAX}`,
  ]
    .filter(Boolean)
    .join(" ");

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
    <>
      <PageBar
        title="Activity"
        actions={
          <Button
            small
            onClick={() => download("dock-audit.csv", toCsv(shown))}
          >
            Export CSV
          </Button>
        }
      />
      <Page gap="gap-4">
        <form
          aria-label="Filters"
          className="flex flex-wrap items-center gap-2.5"
          onSubmit={(e) => e.preventDefault()}
        >
          <fieldset
            aria-label="Actor type"
            className={`seg ${control} *:flex *:items-center`}
          >
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
            className={control}
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
            className={control}
          >
            <option value="">any action</option>
            {[...actions, ...(search.platform ? platformActions : [])].map(
              ([prefix, label]) => (
                <option key={prefix} value={prefix}>
                  {label}
                </option>
              ),
            )}
          </select>
          {!search.platform && (
            <select
              aria-label="Project"
              value={search.project ?? ""}
              onChange={(e) =>
                set({
                  project: e.target.value || undefined,
                  environment: undefined,
                })
              }
              className={control}
            >
              <option value="">all projects</option>
              {projects.map((p) => (
                <option key={p}>{p}</option>
              ))}
            </select>
          )}
          {search.project && (
            <select
              aria-label="Environment"
              value={search.environment ?? ""}
              onChange={(e) =>
                set({ environment: e.target.value || undefined })
              }
              className={control}
            >
              <option value="">all environments</option>
              {environments.map((e) => (
                <option key={e}>{e}</option>
              ))}
            </select>
          )}
          <input
            aria-label="Search"
            placeholder="search target"
            value={search.q ?? ""}
            onChange={(e) => set({ q: e.target.value || undefined })}
            className={`field ${control} max-w-[240px] min-w-[140px] flex-[1_1_140px] py-0 text-xs`}
          />
          <button type="button" onClick={() => onSearch({})} className="lnk">
            Clear
          </button>
        </form>
        <p>
          Last 24h
          {search.project
            ? ` in ${search.project}${search.environment ? `/${search.environment}` : ""}`
            : ""}
          :{" "}
          <b className="font-semibold">
            {deploys.length} {deploys.length === 1 ? "deploy" : "deploys"}
            {byTokens > 0 && `, ${byTokens} by tokens`}
          </b>
          , {byPeople} {byPeople === 1 ? "change" : "changes"} by people.
        </p>
        <AuditTable events={shown} />
        <div className="flex flex-wrap items-center gap-3 text-xs text-mut">
          <span>
            the newest {search.limit ?? PAGE} events
            {search.action ? " of this kind" : ""}
          </span>
          {!search.limit && events.length === PAGE && (
            <Act small cli={cli} onClick={() => set({ limit: MAX })}>
              Show up to {MAX}
            </Act>
          )}
        </div>
      </Page>
    </>
  );
}
