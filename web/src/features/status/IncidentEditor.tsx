import { useSuspenseQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { useIsAdmin, useSession } from "../../data/queries";
import { useOrg } from "../../shell/location";
import { Dialog } from "../../ui/Dialog";
import { clock, day } from "../../ui/format";
import {
  Act,
  Actor,
  actorKind,
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
  type IncidentState,
  impacts,
  incidentState,
  type PostState,
  type StatusImpact,
  type StatusIncident,
  type StatusPage,
  statusQueries,
  statusState,
  useDeleteIncident,
  useEditIncidentUpdate,
  useOpenIncident,
  usePostIncidentUpdate,
  useScheduleMaintenance,
  useUpdateIncident,
} from "./data";
import { Sources } from "./parts";

// Incidents on a status page (ADR 0073, editor mock E2). What is posted
// is public at once; drafts, authors, alerts and the services behind the
// components stay in Dock.

/** When something happened, in the viewer's time: "Nov 11 14:02". */
const when = (iso: string) => `${day(iso)} ${clock(iso)}`;

/** The states an update posts, in the order an incident goes through them. */
const postStates = [
  "investigating",
  "identified",
  "monitoring",
  "resolved",
] as const satisfies IncidentState[];

/** The states an incident may open in, besides draft. */
const openStates = [
  "investigating",
  "identified",
  "monitoring",
] as const satisfies IncidentState[];
type OpenState = (typeof openStates)[number];

/**
 * The states an update may post on a maintenance window, by where it is:
 * a note in its state, or a move on.
 */
const windowStates: Partial<Record<IncidentState, PostState[]>> = {
  scheduled: ["scheduled", "in_progress", "cancelled"],
  in_progress: ["in_progress", "completed", "cancelled"],
};

/** What posting moves a window to, on its button. */
const windowAction: Partial<Record<PostState, string>> = {
  scheduled: "Post note",
  in_progress: "Start now",
  completed: "Complete now",
  cancelled: "Cancel window",
};

/** The `dock maintenance` command for each move. */
const windowVerb: Partial<Record<PostState, string>> = {
  in_progress: "start",
  completed: "complete",
  cancelled: "cancel",
};

/** A time as a datetime-local input's value, in the viewer's zone. */
const toLocalInput = (iso: string) => {
  const d = new Date(iso);
  return new Date(d.getTime() - d.getTimezoneOffset() * 60_000)
    .toISOString()
    .slice(0, 16);
};

/** A datetime-local input's value as an ISO time. */
const fromLocalInput = (v: string) => new Date(v).toISOString();

/** A datetime-local input's value in UTC for a CLI hint, as typed until valid. */
const cliTime = (v: string) =>
  Number.isNaN(Date.parse(v)) ? v : `${fromLocalInput(v).slice(0, 16)}Z`;

/** A window's time as "Nov 13 02:00 to 02:30". */
export const windowText = (inc: StatusIncident) =>
  inc.ends_at
    ? `${when(inc.started_at)} to ${clock(inc.ends_at)}`
    : when(inc.started_at);

/** --component flags for an incident's impact. */
const componentFlags = (ims: StatusImpact[]) =>
  ims.map(
    (im) => `--component "${im.component}:${im.impact.replace("_outage", "")}"`,
  );

/** One impact a choice can be: the component's name and how bad. */
function ImpactsEditor({
  page,
  value,
  onChange,
}: {
  page: StatusPage;
  value: StatusImpact[];
  onChange: (next: StatusImpact[]) => void;
}) {
  const free = page.components.filter(
    (c) => !value.some((im) => im.component === c.name),
  );
  const set = (i: number, im: StatusImpact) =>
    onChange(value.map((x, j) => (j === i ? im : x)));
  return (
    <div className="flex flex-col gap-1.5">
      {value.map((im, i) => (
        <div key={im.component} className="flex items-center gap-2">
          <select
            className="field w-56 py-1 text-sm"
            aria-label="Component"
            value={im.component}
            onChange={(e) => set(i, { ...im, component: e.target.value })}
          >
            {[im.component, ...free.map((c) => c.name)].map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </select>
          <select
            className="field py-1 text-sm"
            aria-label={`Impact on ${im.component}`}
            value={im.impact}
            onChange={(e) =>
              set(i, {
                ...im,
                impact: e.target.value as StatusImpact["impact"],
              })
            }
          >
            {impacts.map((s) => (
              <option key={s} value={s}>
                {statusState[s].label}
              </option>
            ))}
          </select>
          <Button
            small
            aria-label={`Drop ${im.component}`}
            onClick={() => onChange(value.filter((_, j) => j !== i))}
          >
            ×
          </Button>
        </div>
      ))}
      {free[0] && (
        <div>
          <Button
            small
            onClick={() =>
              onChange([
                ...value,
                { component: free[0]?.name ?? "", impact: "degraded" },
              ])
            }
          >
            + component
          </Button>
        </div>
      )}
    </div>
  );
}

/** A plain text box for an update's words. */
function MessageBox({
  value,
  onChange,
  label,
}: {
  value: string;
  onChange: (s: string) => void;
  label: string;
}) {
  return (
    <textarea
      className="field min-h-24 w-full text-sm"
      aria-label={label}
      value={value}
      maxLength={5000}
      rows={4}
      placeholder="What visitors read: what is affected, what you are doing, when you'll say more."
      onChange={(e) => onChange(e.target.value)}
    />
  );
}

/**
 * Opens an incident: posted at once, or saved as a draft for someone to
 * publish.
 */
export function NewIncidentDialog({
  page,
  onClose,
}: {
  page: StatusPage;
  onClose: () => void;
}) {
  const open = useOpenIncident(page.slug);
  const navigate = useNavigate();
  const org = useOrg();
  const [title, setTitle] = useState("");
  const [message, setMessage] = useState("");
  const [state, setState] = useState<OpenState>("investigating");
  const first = page.components[0];
  const [ims, setIms] = useState<StatusImpact[]>(
    first ? [{ component: first.name, impact: "degraded" }] : [],
  );
  const submit = (draft: boolean) =>
    open.mutate(
      {
        title,
        message,
        components: ims,
        state: draft ? "draft" : state,
      },
      {
        onSuccess: (inc) =>
          navigate({
            to: "/$org/status-pages/$page/incidents/$incident",
            params: { org, page: page.slug, incident: inc.id },
          }),
      },
    );
  const cli = [
    `dock incident open ${page.slug} "${title || "<title>"}"`,
    ...componentFlags(ims),
    state !== "investigating" ? `--state ${state}` : "",
    `-m "${message ? "..." : "<message>"}"`,
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <Dialog title="New incident" onClose={onClose} wide>
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          submit(false);
        }}
      >
        <label className="flex flex-col gap-1 text-xs text-mut">
          Title, public
          <input
            className="field text-sm text-fg"
            value={title}
            maxLength={120}
            placeholder="API errors for some requests"
            onChange={(e) => setTitle(e.target.value)}
          />
        </label>
        <div className="flex flex-col gap-1">
          <span className="text-xs text-mut">Affects, while open</span>
          <ImpactsEditor page={page} value={ims} onChange={setIms} />
        </div>
        <label className="flex flex-col gap-1 text-xs text-mut">
          State
          <select
            className="field w-48 py-1 text-sm text-fg"
            value={state}
            onChange={(e) => setState(e.target.value as OpenState)}
          >
            {openStates.map((s) => (
              <option key={s} value={s}>
                {incidentState[s].label}
              </option>
            ))}
          </select>
        </label>
        <MessageBox
          value={message}
          onChange={setMessage}
          label="First update"
        />
        <DialogFoot
          cli={cli}
          note={
            open.error ? (
              <ErrorText error={open.error} />
            ) : (
              "Post makes it public at once; a draft stays in Dock."
            )
          }
        >
          <Button onClick={onClose}>Cancel</Button>
          <Button
            disabled={!title.trim() || open.isPending}
            onClick={() => submit(true)}
          >
            Save draft
          </Button>
          <Button
            type="submit"
            primary
            disabled={!title.trim() || !message.trim() || open.isPending}
          >
            Post
          </Button>
        </DialogFoot>
      </form>
    </Dialog>
  );
}

/**
 * One incident (mock E2): the form to post an update with its impact,
 * the timeline with who did what, and what the components it names say
 * now. Readers read it; people who deploy post; admins remove it.
 */
export function IncidentEditor({ slug, id }: { slug: string; id: string }) {
  const page = useSuspenseQuery(statusQueries.page(slug)).data;
  const org = useOrg();
  const inc = page.incidents.find((i) => i.id === id);
  const parent = (
    <Link to="/$org/status-pages/$page" params={{ org, page: slug }}>
      {page.title}
    </Link>
  );
  if (!inc)
    return (
      <>
        <PageBar parent={parent} title="Incident not found" />
        <Page>
          <p className="text-mut">
            Incident {id} isn't on {page.title}, or was resolved more than 90
            days ago: <span className="font-mono">dock incident show {id}</span>
          </p>
        </Page>
      </>
    );
  return <IncidentView page={page} inc={inc} parent={parent} />;
}

function IncidentView({
  page,
  inc,
  parent,
}: {
  page: StatusPage;
  inc: StatusIncident;
  parent: React.ReactNode;
}) {
  const admin = useIsAdmin();
  const canPost = useSession()?.scope !== "read";
  const navigate = useNavigate();
  const org = useOrg();
  const rename = useUpdateIncident(inc.id);
  const remove = useDeleteIncident(inc.id);
  const [title, setTitle] = useState<string | null>(null);
  const [asking, setAsking] = useState(false);
  return (
    <>
      <PageBar
        parent={parent}
        title={inc.title}
        after={<StateText state={incidentState[inc.state]} />}
        actions={
          <>
            {inc.url && (
              <a
                className="btn"
                href={inc.url}
                target="_blank"
                rel="noreferrer"
              >
                Open public page ↗
              </a>
            )}
            {canPost && (
              <Button onClick={() => setTitle(inc.title)}>Edit title</Button>
            )}
            {admin && (
              <Act
                danger
                cli={`dock incident remove ${inc.id}`}
                onClick={() => setAsking(true)}
              >
                Delete
              </Act>
            )}
          </>
        }
      />
      <Page>
        {title !== null && (
          <form
            className="flex items-center gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              rename.mutate({ title }, { onSuccess: () => setTitle(null) });
            }}
          >
            <input
              className="field w-96"
              aria-label="Title"
              value={title}
              maxLength={120}
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
              cli={`dock incident edit ${inc.id} --title "${title}"`}
              disabled={!title.trim() || rename.isPending}
            >
              Save
            </Act>
            <ErrorText error={rename.error} />
          </form>
        )}
        {canPost && !inc.resolved_at && <PostUpdate page={page} inc={inc} />}
        <Timeline inc={inc} canPost={canPost} />
        <Affected page={page} inc={inc} />
      </Page>
      {asking && (
        <Confirm
          danger
          action={`Delete ${inc.title}`}
          cli={`dock incident remove ${inc.id} --yes`}
          pending={remove.isPending}
          error={remove.error}
          onConfirm={() =>
            remove.mutate(undefined, {
              onSuccess: () =>
                navigate({
                  to: "/$org/status-pages/$page",
                  params: { org, page: page.slug },
                }),
            })
          }
          onCancel={() => setAsking(false)}
        >
          It goes from the page, its history and the feed. The audit log keeps
          that it was there.
        </Confirm>
      )}
    </>
  );
}

/**
 * Posting an update: its state, words and impact. On a draft, the words
 * start as the draft's and posting publishes it; the draft can be saved
 * instead.
 */
function PostUpdate({ page, inc }: { page: StatusPage; inc: StatusIncident }) {
  if (inc.kind === "maintenance") return <PostWindowUpdate inc={inc} />;
  const draft = inc.state === "draft";
  const post = usePostIncidentUpdate(inc.id);
  const save = useUpdateIncident(inc.id);
  const [state, setState] = useState<(typeof postStates)[number]>(
    draft ? "investigating" : (inc.state as (typeof postStates)[number]),
  );
  const [message, setMessage] = useState(draft ? (inc.draft ?? "") : "");
  const [ims, setIms] = useState<StatusImpact[]>(inc.components);
  const changed =
    JSON.stringify(ims) !== JSON.stringify(inc.components) || draft;
  const cli = [
    draft
      ? `dock incident publish ${inc.id}`
      : `dock incident update ${inc.id}`,
    `--state ${state}`,
    `-m "${message ? "..." : "<message>"}"`,
    ...(changed ? componentFlags(ims) : []),
  ].join(" ");
  const pending = post.isPending || save.isPending;
  return (
    <section>
      <SectionHead>{draft ? "Publish" : "Post an update"}</SectionHead>
      <form
        className="flex flex-col gap-3 border-t border-line pt-3"
        onSubmit={(e) => {
          e.preventDefault();
          post.mutate(
            { state, message, components: changed ? ims : undefined },
            { onSuccess: () => setMessage("") },
          );
        }}
      >
        <div className="flex flex-wrap gap-1.5" role="radiogroup">
          {postStates.map((s) => (
            <Button
              key={s}
              small
              role="radio"
              aria-checked={state === s}
              primary={state === s}
              onClick={() => setState(s)}
            >
              {incidentState[s].label}
            </Button>
          ))}
        </div>
        <MessageBox value={message} onChange={setMessage} label="Update" />
        <ImpactsEditor page={page} value={ims} onChange={setIms} />
        <div className="flex flex-wrap items-center gap-2">
          <span className="min-w-0 flex-1 text-xs text-mut">
            {draft
              ? "Publishing makes it public: the title, these words and the components named."
              : "Public as soon as you post."}
          </span>
          <ErrorText error={post.error ?? save.error} />
          {draft && (
            <Act
              cli={`dock incident edit ${inc.id} --draft-message "..."`}
              disabled={pending}
              onClick={() => save.mutate({ draft: message, components: ims })}
            >
              Save draft
            </Act>
          )}
          <Act
            primary
            type="submit"
            cli={cli}
            disabled={!message.trim() || pending}
          >
            {draft
              ? "Publish"
              : state === "resolved"
                ? "Resolve"
                : "Post update"}
          </Act>
        </div>
      </form>
    </section>
  );
}

/** The updates newest first, then how the incident began. */
function Timeline({ inc, canPost }: { inc: StatusIncident; canPost: boolean }) {
  const edit = useEditIncidentUpdate(inc.id);
  const [editing, setEditing] = useState<{ id: number; text: string } | null>(
    null,
  );
  const updates = [...inc.updates].reverse();
  return (
    <section>
      <SectionHead>Timeline</SectionHead>
      <div className="flex flex-col border-t border-line">
        {updates.map((u) => (
          <div
            key={u.id}
            className="grid grid-cols-[110px_1fr] gap-3 border-b border-line py-2.5"
          >
            <span className="font-mono text-sec tabular-nums">
              {when(u.at)}
            </span>
            <div className="flex min-w-0 flex-col gap-1">
              <span className="flex flex-wrap items-baseline gap-x-2.5">
                <StateText state={incidentState[u.state]} />
                <Actor name={u.by} kind={actorKind(u.by)} />
                {u.edited_at && (
                  <span className="text-xs text-faint">
                    edited {when(u.edited_at)}
                  </span>
                )}
                {canPost && editing?.id !== u.id && (
                  <Button
                    small
                    className="ml-auto"
                    onClick={() => setEditing({ id: u.id, text: u.message })}
                  >
                    Edit
                  </Button>
                )}
              </span>
              {editing?.id === u.id ? (
                <form
                  className="flex flex-col gap-2"
                  onSubmit={(e) => {
                    e.preventDefault();
                    edit.mutate(
                      { update: u.id, message: editing.text },
                      { onSuccess: () => setEditing(null) },
                    );
                  }}
                >
                  <MessageBox
                    value={editing.text}
                    label="Update"
                    onChange={(text) => setEditing({ id: u.id, text })}
                  />
                  <div className="flex items-center gap-2">
                    <span className="flex-1 text-xs text-mut">
                      The page says it was edited; the audit log keeps what it
                      said.
                    </span>
                    <ErrorText error={edit.error} />
                    <Button small onClick={() => setEditing(null)}>
                      Cancel
                    </Button>
                    <Act
                      small
                      primary
                      type="submit"
                      cli={`dock incident edit ${inc.id} --update ${u.id} -m "..."`}
                      disabled={!editing.text.trim() || edit.isPending}
                    >
                      Save
                    </Act>
                  </div>
                </form>
              ) : (
                <p className="whitespace-pre-wrap">{u.message}</p>
              )}
            </div>
          </div>
        ))}
        <div className="grid grid-cols-[110px_1fr] gap-3 py-2.5">
          <span className="font-mono text-sec tabular-nums">
            {when(inc.started_at)}
          </span>
          <span className="text-sec">
            {inc.kind === "maintenance" ? (
              <>
                Scheduled by{" "}
                <Actor name={inc.created_by} kind={actorKind(inc.created_by)} />{" "}
                for {windowText(inc)}
              </>
            ) : inc.alert ? (
              <>
                Drafted by {inc.created_by}, from alert {inc.alert.kind} on{" "}
                <span className="font-mono">
                  {inc.alert.service ?? inc.alert.title}
                </span>
              </>
            ) : (
              <>
                {inc.state === "draft" || inc.updates[0]?.by !== inc.created_by
                  ? "Drafted by "
                  : "Opened by "}
                <Actor name={inc.created_by} kind={actorKind(inc.created_by)} />
              </>
            )}
          </span>
        </div>
      </div>
    </section>
  );
}

/**
 * What the components the incident names say now and what they are
 * built from (private), so whoever posts sees what is still broken.
 */
function Affected({ page, inc }: { page: StatusPage; inc: StatusIncident }) {
  const named = page.components.filter((c) =>
    inc.components.some((im) => im.component === c.name),
  );
  if (named.length === 0) return null;
  return (
    <section>
      <SectionHead aside="private">Affected now</SectionHead>
      <Panel>
        <table>
          <tbody>
            {named.map((c) => (
              <tr key={c.id}>
                <td className="py-2 font-medium">{c.name}</td>
                <td>
                  <StateText state={statusState[c.status]} />
                </td>
                <td className="font-mono whitespace-normal text-sec">
                  <Sources component={c} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Panel>
    </section>
  );
}

/**
 * A maintenance window's controls: a note, starting, completing or
 * cancelling it, and moving its times.
 */
function PostWindowUpdate({ inc }: { inc: StatusIncident }) {
  const post = usePostIncidentUpdate(inc.id);
  const move = useUpdateIncident(inc.id);
  const states = windowStates[inc.state] ?? [];
  const [state, setState] = useState<PostState>(states[0] ?? "scheduled");
  const [message, setMessage] = useState("");
  const [starts, setStarts] = useState(toLocalInput(inc.started_at));
  const [ends, setEnds] = useState(toLocalInput(inc.ends_at ?? inc.started_at));
  const cli =
    state === inc.state
      ? `dock incident update ${inc.id} -m "..."`
      : `dock maintenance ${windowVerb[state] ?? "update"} ${inc.id} -m "..."`;
  return (
    <section>
      <SectionHead>Post an update</SectionHead>
      <form
        className="flex flex-col gap-3 border-t border-line pt-3"
        onSubmit={(e) => {
          e.preventDefault();
          post.mutate({ state, message }, { onSuccess: () => setMessage("") });
        }}
      >
        <div className="flex flex-wrap gap-1.5" role="radiogroup">
          {states.map((s) => (
            <Button
              key={s}
              small
              role="radio"
              aria-checked={state === s}
              primary={state === s}
              onClick={() => setState(s)}
            >
              {s === inc.state ? "Note" : windowAction[s]}
            </Button>
          ))}
        </div>
        <MessageBox value={message} onChange={setMessage} label="Update" />
        <div className="flex flex-wrap items-center gap-2">
          <span className="min-w-0 flex-1 text-xs text-mut">
            Public as soon as you post.
            {state === "completed" &&
              " Alerts held during the window and still open are sent."}
          </span>
          <ErrorText error={post.error} />
          <Act
            primary
            type="submit"
            cli={cli}
            disabled={!message.trim() || post.isPending}
          >
            {state === inc.state ? "Post note" : windowAction[state]}
          </Act>
        </div>
      </form>
      <form
        className="mt-4 flex flex-wrap items-center gap-2.5"
        onSubmit={(e) => {
          e.preventDefault();
          move.mutate({
            starts_at:
              inc.state === "scheduled" ? fromLocalInput(starts) : undefined,
            ends_at: fromLocalInput(ends),
          });
        }}
      >
        {inc.state === "scheduled" && (
          <label className="flex items-center gap-2 text-sec">
            Starts
            <input
              type="datetime-local"
              className="field py-1"
              value={starts}
              onChange={(e) => setStarts(e.target.value)}
            />
          </label>
        )}
        <label className="flex items-center gap-2 text-sec">
          Ends
          <input
            type="datetime-local"
            className="field py-1"
            value={ends}
            onChange={(e) => setEnds(e.target.value)}
          />
        </label>
        <Act
          small
          type="submit"
          cli={`dock maintenance extend ${inc.id} --to ${cliTime(ends)}`}
          disabled={!ends || move.isPending}
        >
          Change times
        </Act>
        <ErrorText error={move.error} />
      </form>
    </section>
  );
}

/**
 * Schedules a maintenance window: public at once, started and completed
 * by Dock at its times.
 */
export function ScheduleMaintenanceDialog({
  page,
  onClose,
}: {
  page: StatusPage;
  onClose: () => void;
}) {
  const schedule = useScheduleMaintenance(page.slug);
  const navigate = useNavigate();
  const org = useOrg();
  const hour = new Date(Math.ceil(Date.now() / 3_600_000) * 3_600_000);
  const [title, setTitle] = useState("");
  const [message, setMessage] = useState("");
  const [starts, setStarts] = useState(toLocalInput(hour.toISOString()));
  const [ends, setEnds] = useState(
    toLocalInput(new Date(hour.getTime() + 3_600_000).toISOString()),
  );
  const [names, setNames] = useState<string[]>([]);
  const cli = [
    `dock maintenance schedule ${page.slug} "${title || "<title>"}"`,
    ...names.map((n) => `--component "${n}"`),
    `--from ${cliTime(starts)} --to ${cliTime(ends)}`,
  ].join(" ");
  return (
    <Dialog title="Schedule maintenance" onClose={onClose} wide>
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          schedule.mutate(
            {
              title,
              message,
              components: names,
              starts_at: fromLocalInput(starts),
              ends_at: fromLocalInput(ends),
            },
            {
              onSuccess: (w) =>
                navigate({
                  to: "/$org/status-pages/$page/incidents/$incident",
                  params: { org, page: page.slug, incident: w.id },
                }),
            },
          );
        }}
      >
        <label className="flex flex-col gap-1 text-xs text-mut">
          Title, public
          <input
            className="field text-sm text-fg"
            value={title}
            maxLength={120}
            placeholder="Database upgrade"
            onChange={(e) => setTitle(e.target.value)}
          />
        </label>
        <div className="flex flex-wrap gap-3">
          <label className="flex flex-col gap-1 text-xs text-mut">
            Starts
            <input
              type="datetime-local"
              className="field text-sm text-fg"
              value={starts}
              onChange={(e) => setStarts(e.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1 text-xs text-mut">
            Ends
            <input
              type="datetime-local"
              className="field text-sm text-fg"
              value={ends}
              onChange={(e) => setEnds(e.target.value)}
            />
          </label>
        </div>
        <div className="flex flex-col gap-1">
          <span className="text-xs text-mut">Takes down</span>
          {page.components.map((c) => (
            <Check
              key={c.id}
              label={c.name}
              checked={names.includes(c.name)}
              onChange={(on) =>
                setNames(
                  on ? [...names, c.name] : names.filter((n) => n !== c.name),
                )
              }
            />
          ))}
        </div>
        <MessageBox value={message} onChange={setMessage} label="Message" />
        <DialogFoot
          cli={cli}
          note={
            schedule.error ? (
              <ErrorText error={schedule.error} />
            ) : (
              "Public at once. While it runs, these components read maintenance, their services' alerts wait for its end and their uptime isn't counted."
            )
          }
        >
          <Button onClick={onClose}>Cancel</Button>
          <Button
            type="submit"
            primary
            disabled={
              !title.trim() ||
              names.length === 0 ||
              !starts ||
              !ends ||
              schedule.isPending
            }
          >
            Schedule
          </Button>
        </DialogFoot>
      </form>
    </Dialog>
  );
}
