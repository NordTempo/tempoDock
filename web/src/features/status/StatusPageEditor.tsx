import { useQuery, useSuspenseQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { Fragment, useState } from "react";
import { useIsAdmin, useSession } from "../../data/queries";
import { useShortcuts } from "../../shell/keys";
import { useOrg } from "../../shell/location";
import { Dialog } from "../../ui/Dialog";
import { clock, day } from "../../ui/format";
import {
  Act,
  Button,
  Check,
  Confirm,
  DialogFoot,
  ErrorText,
  Page,
  PageBar,
  Panel,
  SectionHead,
  StateText,
} from "../../ui/kit";
import {
  incidentState,
  type StatusComponent,
  type StatusComponentInput,
  type StatusPage,
  statusQueries,
  statusState,
  toInputs,
  useDeleteStatusPage,
  useSetStatusComponents,
  useUpdateStatusPage,
} from "./data";
import { NewIncidentDialog } from "./IncidentEditor";
import { impactText, Sources } from "./parts";

/**
 * One status page in Dock (ADR 0073, editor mock E1): its incidents, its
 * components with what each is built from (private), their status now
 * and last 90 days, then the page's settings. People who deploy open and
 * update incidents; admins change the rest; members read it.
 */
export function StatusPageEditor({ slug }: { slug: string }) {
  const page = useSuspenseQuery(statusQueries.page(slug)).data;
  const admin = useIsAdmin();
  const canPost = useSession()?.scope !== "read";
  const org = useOrg();
  const publish = useUpdateStatusPage(slug);
  // The component being edited: its index, or "new".
  const [editing, setEditing] = useState<number | "new" | null>(null);
  const [opening, setOpening] = useState(false);
  useShortcuts([
    ...(admin
      ? [{ key: "n", label: "add component", run: () => setEditing("new") }]
      : []),
    ...(canPost
      ? [{ key: "i", label: "new incident", run: () => setOpening(true) }]
      : []),
  ]);
  const pageState = page.published
    ? statusState[page.status]
    : ({ tone: "mut", glyph: "○", label: "unpublished" } as const);
  return (
    <>
      <PageBar
        parent={
          <Link to="/$org/status-pages" params={{ org }}>
            Status pages
          </Link>
        }
        title={page.title}
        after={<StateText state={pageState} />}
        actions={
          <>
            {page.published && page.url && (
              <a
                className="btn"
                href={page.url}
                target="_blank"
                rel="noreferrer"
              >
                Open page ↗
              </a>
            )}
            {admin && (
              <Act
                cli={`dock status-page ${page.published ? "unpublish" : "publish"} ${slug}`}
                disabled={publish.isPending}
                onClick={() => publish.mutate({ published: !page.published })}
              >
                {page.published ? "Unpublish" : "Publish"}
              </Act>
            )}
            {canPost && (
              <Button onClick={() => setOpening(true)}>
                New incident <kbd>I</kbd>
              </Button>
            )}
            {admin && (
              <Button primary onClick={() => setEditing("new")}>
                Add component <kbd>N</kbd>
              </Button>
            )}
          </>
        }
      />
      <Page>
        <p className="text-sec">
          {page.url ? (
            <>
              <span className="font-mono">
                {page.url.replace(/^https?:\/\//, "")}
              </span>{" "}
              {page.published
                ? "serves this page to anyone."
                : "answers 404 until the page is published."}
            </>
          ) : (
            "No address yet: the control plane's server needs a base domain (dock server update --base-domain)."
          )}
        </p>
        <ErrorText error={publish.error} />
        <Incidents page={page} />
        <Components page={page} admin={admin} onEdit={setEditing} />
        <Settings page={page} admin={admin} />
      </Page>
      {opening && (
        <NewIncidentDialog page={page} onClose={() => setOpening(false)} />
      )}
      {editing !== null && (
        <ComponentDialog
          page={page}
          index={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
        />
      )}
    </>
  );
}

/**
 * The page's incidents: drafts waiting for someone first, as a strip
 * each, then the open ones and those resolved in the last 90 days.
 */
function Incidents({ page }: { page: StatusPage }) {
  const org = useOrg();
  const drafts = page.incidents.filter((i) => i.state === "draft");
  const rest = page.incidents.filter((i) => i.state !== "draft");
  const open = rest.filter((i) => !i.resolved_at).length;
  const link = (id: string) => ({
    to: "/$org/status-pages/$page/incidents/$incident" as const,
    params: { org, page: page.slug, incident: id },
  });
  return (
    <section>
      <SectionHead aside={open > 0 ? `${open} open` : undefined}>
        Incidents
      </SectionHead>
      {drafts.map((d) => (
        <div
          key={d.id}
          className="mb-2 flex flex-wrap items-center gap-x-3 gap-y-1 border-l-2 border-fail bg-fail-bg px-3 py-2"
        >
          <span className="min-w-0 flex-1">
            {d.alert ? (
              <>
                <span className="font-mono">
                  {d.alert.service ?? d.alert.title}
                </span>{" "}
                raised {d.alert.kind}.{" "}
              </>
            ) : (
              `${d.created_by} wrote a draft. `
            )}
            Draft incident <span className="font-medium">{d.title}</span> is
            ready, not public yet.
          </span>
          <Link className="btn sm" {...link(d.id)}>
            Review draft
          </Link>
        </div>
      ))}
      {rest.length === 0 ? (
        drafts.length === 0 && (
          <p className="text-mut">No incidents in the last 90 days.</p>
        )
      ) : (
        <Panel>
          <table>
            <thead>
              <tr>
                <th>Incident</th>
                <th>State</th>
                <th>Components</th>
                <th className="text-right">Started</th>
              </tr>
            </thead>
            <tbody>
              {rest.slice(0, 50).map((i) => (
                <tr key={i.id}>
                  <td className="py-2 font-medium">
                    <Link {...link(i.id)}>{i.title}</Link>
                  </td>
                  <td>
                    <StateText state={incidentState[i.state]} />
                  </td>
                  <td className="whitespace-normal text-sec">
                    {impactText(i.components) || "-"}
                  </td>
                  <td className="text-right font-mono text-sec tabular-nums">
                    {day(i.started_at)} {clock(i.started_at)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </Panel>
      )}
    </section>
  );
}

/** The components table, with ordering and removal for admins. */
function Components({
  page,
  admin,
  onEdit,
}: {
  page: StatusPage;
  admin: boolean;
  onEdit: (index: number) => void;
}) {
  const set = useSetStatusComponents(page.slug);
  const move = (from: number, to: number) => {
    const list = toInputs(page);
    const [c] = list.splice(from, 1);
    if (c) list.splice(to, 0, c);
    set.mutate(list);
  };
  if (page.components.length === 0)
    return (
      <p className="text-mut">
        No components.{" "}
        {admin && "Add one for each part of your product the public relies on."}
      </p>
    );
  return (
    <section>
      <SectionHead>Components</SectionHead>
      <Panel>
        <table>
          <thead>
            <tr>
              <th>Public name</th>
              <th>Shows (private)</th>
              <th>Now</th>
              <th>90 days</th>
              <th className="text-right">Uptime</th>
              {admin && <th />}
            </tr>
          </thead>
          <tbody>
            {page.components.map((c, i) => (
              <Fragment key={c.name}>
                {c.group && c.group !== page.components[i - 1]?.group && (
                  <tr>
                    <td
                      colSpan={admin ? 6 : 5}
                      className="pt-3 font-semibold text-fg"
                    >
                      {c.group}
                    </td>
                  </tr>
                )}
                <tr>
                  <td className="py-2 font-medium">
                    {c.name}
                    {c.description && (
                      <span className="ml-2 font-normal text-mut">
                        {c.description}
                      </span>
                    )}
                  </td>
                  <td className="font-mono whitespace-normal text-sec">
                    <Sources component={c} />
                  </td>
                  <td>
                    <StateText state={statusState[c.status]} />
                  </td>
                  <td>
                    <DayBar days={c.days} />
                  </td>
                  <td className="text-right font-mono tabular-nums">
                    {percent(c.uptime)}
                  </td>
                  {admin && (
                    <td className="py-1.5 text-right whitespace-nowrap">
                      <span className="inline-flex gap-1.5">
                        <Button
                          small
                          aria-label={`Move ${c.name} up`}
                          disabled={i === 0 || set.isPending}
                          onClick={() => move(i, i - 1)}
                        >
                          ↑
                        </Button>
                        <Button
                          small
                          aria-label={`Move ${c.name} down`}
                          disabled={
                            i === page.components.length - 1 || set.isPending
                          }
                          onClick={() => move(i, i + 1)}
                        >
                          ↓
                        </Button>
                        <Button small onClick={() => onEdit(i)}>
                          Edit
                        </Button>
                      </span>
                    </td>
                  )}
                </tr>
              </Fragment>
            ))}
          </tbody>
        </table>
      </Panel>
      <ErrorText error={set.error} />
      <p className="mt-2 text-xs text-mut">
        Public names are what everyone sees: pick ones that don't name your
        services or servers. Days are UTC; gray days had no probes.
      </p>
    </section>
  );
}

/**
 * The last 90 days in thin cells: green, amber below 100%, red below 99%;
 * a day an incident ran is at least amber, red for an outage.
 */
function DayBar({ days }: { days: StatusComponent["days"] }) {
  return (
    <span className="flex gap-px" role="img" aria-label="Last 90 days">
      {days.map((d) => (
        <span
          key={d.date}
          className={`inline-block h-3.5 w-[2px] ${
            d.impact === "major_outage" || d.impact === "partial_outage"
              ? "bg-fail"
              : d.uptime === undefined
                ? d.impact
                  ? "bg-warn"
                  : "bg-line"
                : d.uptime >= 100
                  ? d.impact
                    ? "bg-warn"
                    : "bg-ok"
                  : d.uptime >= 99
                    ? "bg-warn"
                    : "bg-fail"
          }`}
          title={`${d.date}: ${d.uptime === undefined ? "no probes" : percent(d.uptime)}${d.incident ? `, ${d.incident}` : ""}`}
        />
      ))}
    </span>
  );
}

/** A percentage the way the page shows it: 100% only when perfect. */
const percent = (p: number | undefined) =>
  p === undefined
    ? "-"
    : p >= 100
      ? "100%"
      : `${Math.min(p, 99.99).toFixed(2)}%`;

/** The page's title, whether it shows percentages, and removing it. */
function Settings({ page, admin }: { page: StatusPage; admin: boolean }) {
  const update = useUpdateStatusPage(page.slug);
  const remove = useDeleteStatusPage(page.slug);
  const navigate = useNavigate();
  const org = useOrg();
  const [title, setTitle] = useState<string | null>(null);
  const [asking, setAsking] = useState(false);
  return (
    <section>
      <SectionHead>Settings</SectionHead>
      <div className="flex flex-col gap-4 border-t border-line pt-3">
        <div className="flex flex-wrap items-center gap-2.5">
          <span className="w-40 text-sec">Title</span>
          {title === null ? (
            <>
              <span>{page.title}</span>
              {admin && (
                <Button small onClick={() => setTitle(page.title)}>
                  Edit
                </Button>
              )}
            </>
          ) : (
            <form
              className="flex items-center gap-2"
              onSubmit={(e) => {
                e.preventDefault();
                update.mutate({ title }, { onSuccess: () => setTitle(null) });
              }}
            >
              <input
                className="field w-64"
                aria-label="Title"
                value={title}
                onKeyDown={(e) => e.key === "Escape" && setTitle(null)}
                onChange={(e) => setTitle(e.target.value)}
              />
              <Button small onClick={() => setTitle(null)}>
                Cancel
              </Button>
              <Act
                small
                primary
                type="submit"
                cli={`dock status-page update ${page.slug} --title "${title}"`}
                disabled={!title.trim() || update.isPending}
              >
                Save
              </Act>
            </form>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2.5">
          <span className="w-40 text-sec">Percentages</span>
          <Check
            label="Show uptime percentages; the bars and statuses show either way"
            checked={page.show_uptime}
            disabled={!admin || update.isPending}
            onChange={(on) => update.mutate({ show_uptime: on })}
          />
        </div>
        <div className="flex flex-wrap items-center gap-2.5">
          <span className="w-40 text-sec">Drafts</span>
          <Check
            label="Draft an incident when a component's uptime check goes down or its service crash loops"
            checked={page.drafts}
            disabled={!admin || update.isPending}
            onChange={(on) => update.mutate({ drafts: on })}
          />
        </div>
        <ErrorText error={update.error} />
        {admin && (
          <div>
            <Act
              danger
              cli={`dock status-page remove ${page.slug}`}
              onClick={() => setAsking(true)}
            >
              Remove page
            </Act>
          </div>
        )}
      </div>
      {asking && (
        <Confirm
          danger
          action={`Remove ${page.slug}`}
          cli={`dock status-page remove ${page.slug} --yes`}
          pending={remove.isPending}
          error={remove.error}
          onConfirm={() =>
            remove.mutate(undefined, {
              onSuccess: () =>
                navigate({ to: "/$org/status-pages", params: { org } }),
            })
          }
          onCancel={() => setAsking(false)}
        >
          Its address stops answering at once and its components go. The
          services and their uptime checks stay as they are.
        </Confirm>
      )}
    </section>
  );
}

/**
 * Adds a component or changes one: its public name, group, description
 * and sources, picked from the organization's services and their uptime
 * checks.
 */
function ComponentDialog({
  page,
  index,
  onClose,
}: {
  page: StatusPage;
  index: number | null;
  onClose: () => void;
}) {
  const set = useSetStatusComponents(page.slug);
  const candidates = useQuery(statusQueries.sources());
  const list = toInputs(page);
  const start: StatusComponentInput =
    index === null
      ? { name: "", group: "", description: "", auto: true, sources: [] }
      : (list[index] ?? { name: "" });
  const [c, setC] = useState<StatusComponentInput>(start);
  const sources = c.sources ?? [];
  const has = (svc: string) => sources.some((s) => s.service === svc);
  const hasCheck = (id: number) => sources.some((s) => s.check === id);
  const toggle = (on: boolean, src: { service?: string; check?: number }) =>
    setC({
      ...c,
      sources: on
        ? [...sources, src]
        : sources.filter((s) =>
            src.service ? s.service !== src.service : s.check !== src.check,
          ),
    });
  const save = (next: StatusComponentInput[]) =>
    set.mutate(next, { onSuccess: onClose });
  const cli = [
    `dock status-page component add ${page.slug} "${c.name || "<name>"}"`,
    ...sources.map((s) =>
      s.check ? `--check ${s.check}` : `--service ${s.service}`,
    ),
    c.group ? `--group "${c.group}"` : "",
    c.auto === false ? "--manual" : "",
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <Dialog
      title={index === null ? "Add component" : `Edit ${start.name}`}
      onClose={onClose}
      wide
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          const next = [...list];
          if (index === null) next.push(c);
          else next[index] = c;
          save(next);
        }}
      >
        <label className="flex flex-col gap-1 text-xs text-mut">
          Public name, what everyone sees
          <input
            className="field text-sm text-fg"
            value={c.name}
            maxLength={60}
            placeholder="API"
            onChange={(e) => setC({ ...c, name: e.target.value })}
          />
        </label>
        <div className="flex flex-wrap gap-3">
          <label className="flex flex-col gap-1 text-xs text-mut">
            Group
            <input
              className="field w-48 text-sm text-fg"
              value={c.group ?? ""}
              maxLength={40}
              placeholder="Core"
              list="status-groups"
              onChange={(e) => setC({ ...c, group: e.target.value })}
            />
            <datalist id="status-groups">
              {[
                ...new Set(page.components.map((x) => x.group).filter(Boolean)),
              ].map((g) => (
                <option key={g} value={g} />
              ))}
            </datalist>
          </label>
          <label className="flex min-w-64 flex-1 flex-col gap-1 text-xs text-mut">
            Description, shown next to the name
            <input
              className="field text-sm text-fg"
              value={c.description ?? ""}
              maxLength={200}
              onChange={(e) => setC({ ...c, description: e.target.value })}
            />
          </label>
        </div>
        <Check
          label="Status from the sources' uptime checks and health"
          checked={c.auto !== false}
          onChange={(on) => setC({ ...c, auto: on })}
        />
        <div className="flex flex-col gap-1">
          <span className="text-xs text-mut">
            Shows (private): a service covers all its uptime checks and its
            health; or pick single checks
          </span>
          <div className="max-h-72 overflow-y-auto border-y border-line py-1.5">
            {candidates.isPending && (
              <p className="text-mut">Loading services…</p>
            )}
            <ErrorText error={candidates.error} />
            {candidates.data?.map((svc) => (
              <div key={svc.service} className="py-0.5">
                <Check
                  label={<span className="font-mono">{svc.service}</span>}
                  checked={has(svc.service)}
                  onChange={(on) => toggle(on, { service: svc.service })}
                />
                {svc.checks.map((chk) => (
                  <div key={chk.id} className="pl-6">
                    <Check
                      label={
                        <span className="font-mono text-sec">{chk.url}</span>
                      }
                      checked={hasCheck(chk.id)}
                      disabled={has(svc.service)}
                      onChange={(on) => toggle(on, { check: chk.id })}
                    />
                  </div>
                ))}
              </div>
            ))}
            {candidates.data?.length === 0 && (
              <p className="text-mut">No services yet.</p>
            )}
          </div>
        </div>
        <DialogFoot cli={cli} note={<ErrorText error={set.error} />}>
          {index !== null && (
            <Button
              danger
              disabled={set.isPending}
              onClick={() => save(list.filter((_, i) => i !== index))}
            >
              Remove
            </Button>
          )}
          <Button onClick={onClose}>Cancel</Button>
          <Button
            type="submit"
            primary
            disabled={!c.name.trim() || set.isPending}
          >
            {index === null ? "Add" : "Save"}
          </Button>
        </DialogFoot>
      </form>
    </Dialog>
  );
}
