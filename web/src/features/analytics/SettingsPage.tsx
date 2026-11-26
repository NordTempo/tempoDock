import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { type KeyboardEvent, type ReactNode, useState } from "react";
import { useOrg } from "../../shell/location";
import { Act, Button, CopyButton, ErrorText, SectionHead } from "../../ui/kit";
import {
  type Site,
  type SiteSettings,
  siteGoalsQuery,
  useAddGoal,
  useRemoveGoal,
  useUpdateSite,
} from "./data";
import {
  ConsentSnippet,
  ConsentWarning,
  PolicyText,
  PrivacyStatus,
  PrivacyTable,
} from "./Privacy";
import {
  type PublicDraft,
  PublicSection,
  publicChange,
  publicDraft,
} from "./PublicSection";
import { count } from "./parts";
import { scriptTag } from "./ServiceSite";
import { SettingInput } from "./SettingInput";
import { useSiteView } from "./SitePage";
import {
  type Field,
  fields,
  flags,
  type Key,
  summary,
  text,
  valid,
  value,
} from "./settings";
import { TrackingStatus } from "./Tracking";

// A site's settings page in the Analytics area (Saile's pick, 2026-11-13;
// it replaced a dialog): the site, what it means for consent (ADR 0085),
// its hostnames, goals, what it collects, replay, retention and sharing. Rows in the app's settings model: the
// value as text and one Edit that opens a small form, Save or Esc. Each
// setting is the site's own or its organization's default.

/** The settings in each section, in settings.ts's words. */
const sections: { id: string; title: string; keys: Key[] }[] = [
  {
    id: "set-collection",
    title: "Collection",
    keys: ["excluded_paths", "ignored_ips", "errors_and_vitals"],
  },
  {
    id: "set-replay",
    title: "Session replay",
    keys: ["replay", "replay_sample_rate", "mask_all_text"],
  },
  {
    id: "set-retention",
    title: "Retention",
    keys: [
      "event_retention_days",
      "error_retention_days",
      "replay_retention_days",
    ],
  },
];

const fieldOf = Object.fromEntries(fields.map((f) => [f.key, f])) as Record<
  Key,
  Field
>;

export function SettingsPage() {
  const { project, site, canEdit } = useSiteView();
  const cmd = `dock analytics site set ${site.name} -p ${project}`;
  return (
    <div className="flex flex-col gap-8">
      <ConsentWarning
        project={project}
        site={site}
        cmd={cmd}
        canEdit={canEdit}
      />
      <Section id="set-site" title="Site">
        <NameRow project={project} site={site} canEdit={canEdit} />
        <Row label="Environment" hint="Its hostnames come from here.">
          <span className="font-mono">
            {project} / {site.environment}
          </span>
        </Row>
        <Row label="Script" hint="On every page of the site.">
          <span className="flex flex-wrap items-center gap-3">
            <code className="text-xs break-all">{scriptTag}</code>
            <CopyButton text={scriptTag} />
            <TrackingStatus project={project} site={site.name} />
          </span>
        </Row>
      </Section>
      <Section id="set-privacy" title="Privacy">
        <Row label="Status" hint="Not legal advice.">
          <PrivacyStatus site={site} />
        </Row>
        <Row label="Collects" hint="Mention all of it in your privacy policy.">
          <PrivacyTable site={site} />
        </Row>
        <SettingRow
          project={project}
          site={site}
          field={fieldOf.consent}
          cmd={cmd}
          canEdit={canEdit}
        />
        <Row label="In your banner" hint="Gives or withdraws consent.">
          <ConsentSnippet site={site} />
        </Row>
        <Row label="Privacy policy" hint="From this site's settings.">
          <PolicyText site={site} />
        </Row>
      </Section>
      <Hostnames project={project} site={site} />
      <Goals project={project} site={site.name} canEdit={canEdit} />
      {sections.map((s) => (
        <Section key={s.id} id={s.id} title={s.title}>
          {s.keys.map((key) => (
            <SettingRow
              key={key}
              project={project}
              site={site}
              field={fieldOf[key]}
              cmd={cmd}
              canEdit={canEdit}
            />
          ))}
        </Section>
      ))}
      <Section id="set-sharing" title="Sharing">
        <SharingRow project={project} site={site} cmd={cmd} canEdit={canEdit} />
      </Section>
    </div>
  );
}

function Section({
  id,
  title,
  children,
}: {
  id: string;
  title: string;
  children: ReactNode;
}) {
  return (
    <section aria-labelledby={id} id={id}>
      <SectionHead id={`${id}-h`}>{title}</SectionHead>
      <dl className="props">{children}</dl>
    </section>
  );
}

/** A label with its short line, and what it holds. */
function Row({
  label,
  hint,
  children,
}: {
  label: ReactNode;
  hint?: string;
  children: ReactNode;
}) {
  return (
    <>
      <dt>
        <span className="block text-sec">{label}</span>
        {hint && <span className="block text-2xs text-faint">{hint}</span>}
      </dt>
      <dd>{children}</dd>
    </>
  );
}

/**
 * A row whose value opens a form with Edit. `form` gets `close`; the form
 * saves and closes itself, and closes on Esc (escapes). Read-only people
 * see the value.
 */
function EditRow({
  label,
  hint,
  shown,
  canEdit,
  form,
}: {
  label: string;
  hint?: string;
  shown: ReactNode;
  canEdit: boolean;
  form: (close: () => void) => ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const close = () => setOpen(false);
  return (
    <Row label={label} hint={hint}>
      {open ? (
        form(close)
      ) : (
        <span className="flex max-w-[760px] items-start gap-3">
          <span className="min-w-0 flex-1 break-words">{shown}</span>
          {canEdit && (
            <Button
              small
              aria-label={`Edit ${label.toLowerCase()}`}
              onClick={() => setOpen(true)}
            >
              Edit
            </Button>
          )}
        </span>
      )}
    </Row>
  );
}

/** A form's Esc: cancels it, like Cancel. */
const escapes = (cancel: () => void) => (e: KeyboardEvent) => {
  if (e.key === "Escape") cancel();
};

/** Save and Cancel at the end of a row's form, with the error under them. */
function FormEnd({
  cli,
  disabled,
  pending,
  error,
  onCancel,
}: {
  cli: string;
  disabled: boolean;
  pending: boolean;
  error: Error | null;
  onCancel: () => void;
}) {
  return (
    <>
      <span className="flex gap-2">
        <Act
          small
          primary
          type="submit"
          cli={cli}
          disabled={disabled || pending}
        >
          {pending ? "Saving…" : "Save"}
        </Act>
        <Button small onClick={onCancel}>
          Cancel
        </Button>
      </span>
      <ErrorText error={error} />
    </>
  );
}

/** The site's name; renaming keeps the page open under the new one. */
function NameRow({
  project,
  site,
  canEdit,
}: {
  project: string;
  site: Site;
  canEdit: boolean;
}) {
  const org = useOrg();
  const navigate = useNavigate();
  const update = useUpdateSite();
  return (
    <EditRow
      label="Name"
      hint="What Dock calls it."
      shown={<span className="font-mono">{site.name}</span>}
      canEdit={canEdit}
      form={(close) => (
        <NameForm
          name={site.name}
          pending={update.isPending}
          error={update.error}
          cli={(n) =>
            `dock analytics site set ${site.name} -p ${project} --name ${n}`
          }
          onCancel={close}
          onSave={(name) =>
            update.mutate(
              { project, site: site.name, change: { name } },
              {
                onSuccess: () =>
                  navigate({
                    to: "/$org/analytics/$project/$site/settings",
                    params: { org, project, site: name },
                    replace: true,
                  }),
              },
            )
          }
        />
      )}
    />
  );
}

function NameForm({
  name,
  pending,
  error,
  cli,
  onCancel,
  onSave,
}: {
  name: string;
  pending: boolean;
  error: Error | null;
  cli: (name: string) => string;
  onCancel: () => void;
  onSave: (name: string) => void;
}) {
  const [draft, setDraft] = useState(name);
  const next = draft.trim();
  return (
    <form
      aria-label="Edit name"
      onKeyDown={escapes(onCancel)}
      className="flex flex-wrap items-center gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        onSave(next);
      }}
    >
      <input
        // biome-ignore lint/a11y/noAutofocus: opened by Edit
        autoFocus
        aria-label="Name"
        className="field w-72 font-mono"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
      />
      <FormEnd
        cli={cli(next || "<name>")}
        disabled={!next || next === name}
        pending={pending}
        error={error}
        onCancel={onCancel}
      />
    </form>
  );
}

/** A setting as a row shows it: a list's entries, "30 days", "on". */
function shownValue(s: SiteSettings, key: Key) {
  const v = s[key];
  if (Array.isArray(v)) return v.length ? v.join(", ") : "none";
  const t = summary(s, key);
  return t === "on" ? "On" : t === "off" ? "Off" : t;
}

/**
 * One site setting: its value, "(default)" while it is the organization's.
 * The form chooses Default or Custom, as the dialog it replaced did.
 */
function SettingRow({
  project,
  site,
  field,
  cmd,
  canEdit,
}: {
  project: string;
  site: Site;
  field: Field;
  cmd: string;
  canEdit: boolean;
}) {
  const own = site.overrides[field.key] !== undefined;
  const shown = shownValue(site.settings, field.key);
  return (
    <EditRow
      label={field.label}
      hint={field.help}
      shown={
        own ? (
          shown
        ) : (
          <>
            {shown} <span className="text-faint">default</span>
          </>
        )
      }
      canEdit={canEdit}
      form={(close) => (
        <SettingForm
          project={project}
          site={site}
          field={field}
          cmd={cmd}
          onDone={close}
        />
      )}
    />
  );
}

function SettingForm({
  project,
  site,
  field,
  cmd,
  onDone,
}: {
  project: string;
  site: Site;
  field: Field;
  cmd: string;
  onDone: () => void;
}) {
  const { key } = field;
  const update = useUpdateSite();
  const was = site.overrides[key] !== undefined;
  const [custom, setCustom] = useState(was);
  const [draft, setDraft] = useState(() => text(site.settings, key));
  const changed = custom ? !was || draft !== text(site.settings, key) : was;
  const ok = !custom || valid(key, draft);
  const cli = custom
    ? `${cmd} ${flags(key, draft).join(" ")}`
    : `${cmd} --reset ${key}`;
  return (
    <form
      aria-label={`Edit ${field.label.toLowerCase()}`}
      onKeyDown={escapes(onDone)}
      className="flex flex-col gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        update.mutate(
          {
            project,
            site: site.name,
            change: custom ? { settings: value(key, draft) } : { reset: [key] },
          },
          { onSuccess: onDone },
        );
      }}
    >
      <span className="seg self-start">
        <button
          type="button"
          aria-pressed={!custom}
          onClick={() => setCustom(false)}
        >
          Default
        </button>
        <button
          type="button"
          aria-pressed={custom}
          onClick={() => setCustom(true)}
        >
          Custom
        </button>
      </span>
      {custom && (
        <SettingInput field={field} value={draft} onChange={setDraft} />
      )}
      <FormEnd
        cli={cli}
        disabled={!changed || !ok}
        pending={update.isPending}
        error={update.error}
        onCancel={onDone}
      />
    </form>
  );
}

/** The public dashboard: off, at Dock's address or at a domain of its own. */
function SharingRow({
  project,
  site,
  cmd,
  canEdit,
}: {
  project: string;
  site: Site;
  cmd: string;
  canEdit: boolean;
}) {
  const saved = publicDraft(site);
  const where =
    saved.mode === "off"
      ? "Off"
      : saved.mode === "domain"
        ? `On at ${saved.domain}`
        : "On at its Dock address";
  return (
    <EditRow
      label="Public dashboard"
      hint="Read only: the numbers, never replays."
      shown={
        <>
          {where}
          {saved.mode !== "off" && (
            <span className="text-mut">
              , errors {saved.errors ? "shown" : "hidden"}
            </span>
          )}
          {site.public_urls?.[0] && (
            <a
              href={site.public_urls[0]}
              target="_blank"
              rel="noreferrer"
              className="ml-3 text-info"
            >
              open ↗
            </a>
          )}
        </>
      }
      canEdit={canEdit}
      form={(close) => (
        <SharingForm project={project} site={site} cmd={cmd} onDone={close} />
      )}
    />
  );
}

function SharingForm({
  project,
  site,
  cmd,
  onDone,
}: {
  project: string;
  site: Site;
  cmd: string;
  onDone: () => void;
}) {
  const update = useUpdateSite();
  const [draft, setDraft] = useState<PublicDraft>(() => publicDraft(site));
  const change = publicChange(site, draft);
  return (
    <form
      aria-label="Edit public dashboard"
      onKeyDown={escapes(onDone)}
      className="flex flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        update.mutate(
          { project, site: site.name, change: { settings: change.settings } },
          { onSuccess: onDone },
        );
      }}
    >
      <PublicSection
        project={project}
        site={site}
        draft={draft}
        onChange={setDraft}
      />
      <FormEnd
        cli={`${cmd} ${change.flags.join(" ")}`.trim()}
        disabled={!change.valid || change.flags.length === 0}
        pending={update.isPending}
        error={update.error}
        onCancel={onDone}
      />
    </form>
  );
}

/**
 * The service whose visits the site counts and the hostnames that are:
 * its domains that don't redirect (ADR 0104), changed on the service.
 */
function Hostnames({ project, site }: { project: string; site: Site }) {
  const org = useOrg();
  const hostnames = site.hostnames ?? [];
  return (
    <Section id="set-hostnames" title="Hostnames">
      <Row label="Service" hint="Its domains count; redirects don't.">
        {site.service ? (
          <Link
            to="/$org/$project/$environment/$service/settings"
            params={{
              org,
              project,
              environment: site.environment,
              service: site.service,
            }}
            hash="analytics"
          >
            {site.service}
          </Link>
        ) : (
          <span className="text-mut">none</span>
        )}
      </Row>
      {hostnames.map((h) => (
        <Row
          key={h.hostname}
          label={<span className="font-mono">{h.hostname}</span>}
        >
          <span className="text-mut">counted</span>
        </Row>
      ))}
    </Section>
  );
}

/** The CLI command a goal's form stands for. */
const goalCli = (site: string, name: string, kind: string, match: string) =>
  `dock analytics goals add ${site} ${JSON.stringify(name || "Name")} ${kind}:${match || (kind === "page" ? "/path" : "event")}`;

/**
 * The site's goals, with their visitors over 30 days, to remove; and the
 * form for a new one. The Overview's Goals panel shows their conversions.
 */
function Goals({
  project,
  site,
  canEdit,
}: {
  project: string;
  site: string;
  canEdit: boolean;
}) {
  const { data, error } = useQuery(siteGoalsQuery(project, site, "30d", []));
  const remove = useRemoveGoal();
  const goals = data?.goals ?? [];
  return (
    <Section id="set-goals" title="Goals">
      {goals.map((g) => (
        <Row
          key={g.name}
          label={g.name}
          hint={`${g.kind === "page" ? "page" : "event"} ${g.match}`}
        >
          <span className="flex max-w-[760px] items-start gap-3">
            <span className="flex-1 text-mut tabular-nums">
              {count(g.visitors)} visitors in 30 days
            </span>
            {canEdit && (
              <Act
                small
                cli={`dock analytics goals remove ${site} ${JSON.stringify(g.name)}`}
                disabled={remove.isPending}
                onClick={() => remove.mutate({ project, site, goal: g.name })}
              >
                Remove
              </Act>
            )}
          </span>
        </Row>
      ))}
      {goals.length === 0 && (
        <Row label="None yet" hint="A page or a custom event that converts.">
          <span className="text-mut">
            {data ? "" : error ? error.message : "Loading…"}
          </span>
        </Row>
      )}
      {canEdit && (
        <Row label="Add a goal">
          <GoalForm project={project} site={site} />
        </Row>
      )}
      <ErrorText error={remove.error} />
    </Section>
  );
}

/** A new goal: its name, page or event, and what it matches. */
function GoalForm({ project, site }: { project: string; site: string }) {
  const [name, setName] = useState("");
  const [kind, setKind] = useState<"page" | "event">("event");
  const [match, setMatch] = useState("");
  const add = useAddGoal();
  return (
    <form
      className="flex max-w-[760px] flex-col gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        add.mutate(
          { project, site, goal: { name, kind, match } },
          {
            onSuccess: () => {
              setName("");
              setMatch("");
            },
          },
        );
      }}
    >
      <span className="flex flex-wrap items-center gap-2">
        <input
          aria-label="Goal name"
          placeholder="Signed up"
          value={name}
          onChange={(e) => setName(e.target.value)}
          className="field w-40"
        />
        <span className="seg">
          {(["page", "event"] as const).map((k) => (
            <button
              key={k}
              type="button"
              aria-pressed={kind === k}
              onClick={() => setKind(k)}
            >
              {k === "page" ? "Page" : "Event"}
            </button>
          ))}
        </span>
        <input
          aria-label={kind === "page" ? "Page path" : "Event name"}
          placeholder={kind === "page" ? "/welcome (* for any)" : "signup"}
          value={match}
          onChange={(e) => setMatch(e.target.value)}
          className="field w-52 font-mono"
        />
        <Act
          small
          type="submit"
          cli={goalCli(site, name, kind, match)}
          disabled={!name || !match || add.isPending}
        >
          Add goal
        </Act>
      </span>
      <ErrorText error={add.error} />
    </form>
  );
}
