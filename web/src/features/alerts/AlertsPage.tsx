import { useQuery } from "@tanstack/react-query";
import { createContext, useContext, useState } from "react";
import { api, list } from "../../api/client";
import { useIsAdmin } from "../../data/queries";
import { ErrorBlock } from "../../ui/ErrorBlock";
import { since } from "../../ui/format";
import {
  Act,
  Button,
  Confirm,
  CopyButton,
  ErrorText,
  Glyph,
  Page,
  PageBar,
  Panel,
  SectionHead,
} from "../../ui/kit";
import { toneText } from "../../ui/status";
import { useValidation } from "../create/form";
import {
  type Alert,
  type AlertChannel,
  type AlertScope,
  type NewAlertChannel,
  type Severity,
  scopeQuery,
  useAlertChannels,
  useAllAlerts,
  useCreateAlertChannel,
  useOpenAlerts,
  useRemoveAlertChannel,
  useResolveAlert,
  useSetVulnAlerts,
  useTestAlertChannel,
  useVulnAlerts,
  type VulnAlerts,
} from "./data";
import { AlertLink, alertText } from "./source";

// Alerts (ADR 0062): open alerts (worst first), recent history, and the
// channels alerts are sent to, for both scopes: an org's, for its members
// (in its Inbox; channels for its admins under Settings), and the
// platform's, on the console for operators.

/** Resolved alerts shown under the open ones, and on request. */
const HISTORY = 50;
const MAX = 500;

/** The page's scope, for the rows and dialogs under it. */
const Scope = createContext<AlertScope>("org");

/** The CLI flag that picks the scope: --platform for the platform's. */
const flag = (scope: AlertScope) => (scope === "platform" ? " --platform" : "");

/** Up to MAX alerts (`dock alerts --all --limit 500`), fetched on request. */
const useOlderAlerts = (scope: AlertScope, enabled: boolean) =>
  useQuery({
    queryKey: ["alerts", scope, "all", MAX],
    queryFn: () =>
      list(
        api.GET("/api/v1/alerts", {
          params: { query: { ...scopeQuery(scope), all: true, limit: MAX } },
        }),
      ),
    enabled,
  });

/**
 * The console's Alerts page (operators, the platform's alerts): the lists
 * and the settings on one page. An organization's are split: the lists
 * in its Inbox, the settings under Settings, Alerts (ADR 0076).
 */
export function AlertsPage({ scope }: { scope: AlertScope }) {
  return (
    <>
      <PageBar title="Alerts" />
      <Page>
        <AlertLists scope={scope} />
        <AlertSettings scope={scope} />
      </Page>
    </>
  );
}

/**
 * Open alerts (worst first) and the last resolved ones. `skip` leaves
 * some out: the Inbox's access request alerts, which its own section
 * lists for admins.
 */
export function AlertLists({
  scope,
  skip,
}: {
  scope: AlertScope;
  skip?: (a: Alert) => boolean;
}) {
  const kept = (alerts: Alert[]) =>
    skip ? alerts.filter((a) => !skip(a)) : alerts;
  const open = kept(useOpenAlerts(scope));
  const recent = useAllAlerts(scope);
  const [more, setMore] = useState(false);
  const older = useOlderAlerts(scope, more);
  const resolved = kept(older.data ?? recent)
    .filter((a) => a.resolved_at)
    .slice(0, more ? MAX : HISTORY);
  return (
    <Scope.Provider value={scope}>
      <section aria-labelledby="open">
        <SectionHead id="open" aside={open.length > 0 && open.length}>
          Open
        </SectionHead>
        {open.length > 0 ? (
          <AlertTable alerts={open} resolvable />
        ) : (
          <p className="py-2 text-mut">Nothing open.</p>
        )}
      </section>
      <section aria-labelledby="history">
        <SectionHead
          id="history"
          right={
            !more &&
            resolved.length === HISTORY && (
              <Act
                small
                cli={`dock alerts --all --limit ${MAX}${flag(scope)}`}
                onClick={() => setMore(true)}
              >
                Show up to {MAX}
              </Act>
            )
          }
        >
          {/* "Last 50" only once the list is cut there. */}
          {more || resolved.length === HISTORY
            ? `Last ${more ? MAX : HISTORY} resolved`
            : "Resolved"}
        </SectionHead>
        {resolved.length > 0 ? (
          <AlertTable alerts={resolved} />
        ) : (
          <p className="py-2 text-mut">None yet.</p>
        )}
        {older.error && (
          <ErrorBlock error={older.error} onRetry={() => older.refetch()} />
        )}
      </section>
      <p className="font-mono text-2xs text-mut">dock alerts{flag(scope)}</p>
    </Scope.Provider>
  );
}

/**
 * Where alerts go and what alerts: an org's vulnerability setting
 * (ADR 0074) and, for its admins, its channels; the platform's channels
 * on the console.
 */
export function AlertSettings({ scope }: { scope: AlertScope }) {
  // An org's channels are for its admins; operators manage the platform's.
  const admin = useIsAdmin();
  return (
    <Scope.Provider value={scope}>
      {scope === "org" && <VulnerabilityAlerts admin={admin} />}
      {(scope === "platform" || admin) && <Channels />}
    </Scope.Provider>
  );
}

const vulnWords: Record<VulnAlerts, string> = {
  critical: "Alert on new critical vulnerabilities in live images",
  high: "Alert on new critical and high vulnerabilities in live images",
  off: "No alerts on vulnerabilities",
};

/**
 * The org's one vulnerability alert setting (ADR 0074, mock S): what
 * alerts now, and for admins a way to change it.
 */
function VulnerabilityAlerts({ admin }: { admin: boolean }) {
  const alert = useVulnAlerts();
  const set = useSetVulnAlerts();
  const [editing, setEditing] = useState<VulnAlerts>();
  return (
    <section aria-labelledby="vulns" className="flex flex-col gap-1.5">
      <SectionHead id="vulns">Vulnerabilities</SectionHead>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-t border-line pt-2.5">
        {editing ? (
          <>
            <select
              aria-label="Vulnerability alerts"
              value={editing}
              onChange={(e) => setEditing(e.target.value as VulnAlerts)}
              className="py-[5px] text-sm"
            >
              {(Object.keys(vulnWords) as VulnAlerts[]).map((k) => (
                <option key={k} value={k}>
                  {vulnWords[k]}
                </option>
              ))}
            </select>
            <Act
              small
              primary
              cli={`dock alerts vulns ${editing}`}
              disabled={set.isPending}
              onClick={() =>
                set.mutate(editing, { onSuccess: () => setEditing(undefined) })
              }
            >
              Save
            </Act>
            <Button small onClick={() => setEditing(undefined)}>
              Cancel
            </Button>
            <ErrorText error={set.error} />
          </>
        ) : (
          <>
            <span>{vulnWords[alert]}</span>
            {admin && (
              <Button small onClick={() => setEditing(alert)}>
                Edit
              </Button>
            )}
            <span className="font-mono text-2xs text-faint">
              dock alerts vulns critical|high|off
            </span>
          </>
        )}
      </div>
      <p className="text-xs text-mut">
        Resolved by itself when the release that has them is replaced by one
        that doesn't, or they're ignored. No reminders: standing findings live
        on the Images page.
      </p>
    </section>
  );
}

const severityTone = { critical: "fail", warning: "warn" } as const;

/** Where an alert is: project/environment/service, or a server. */
function where(a: Alert) {
  if (a.project && a.environment && a.service) {
    return `${a.project}/${a.environment}/${a.service}`;
  }
  return a.server ?? a.project ?? "";
}

function AlertTable({
  alerts,
  resolvable,
}: {
  alerts: Alert[];
  resolvable?: boolean;
}) {
  return (
    <Panel>
      <table>
        <thead>
          <tr>
            <th>Severity</th>
            <th>Alert</th>
            <th className="pr-7">Where</th>
            <th>{resolvable ? "Open for" : "Resolved"}</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {alerts.map((a) => (
            <AlertRow key={a.id} alert={a} resolvable={resolvable} />
          ))}
        </tbody>
      </table>
    </Panel>
  );
}

function AlertRow({
  alert: a,
  resolvable,
}: {
  alert: Alert;
  resolvable?: boolean;
}) {
  const resolve = useResolveAlert();
  const scope = useContext(Scope);
  const tone = a.resolved_at ? "text-mut" : toneText[severityTone[a.severity]];
  const { text, cli } = alertText(a);
  return (
    <tr>
      <td className={`py-2 whitespace-nowrap ${tone}`}>
        <Glyph char={a.severity === "critical" ? "✕" : "▲"} /> {a.severity}
      </td>
      <td className="w-full min-w-[240px] py-2 whitespace-normal">
        {a.title}
        {text && <span className="block text-mut">{text}</span>}
      </td>
      <td className="py-2 pr-7 font-mono">{where(a)}</td>
      <td className="py-2 whitespace-nowrap text-mut">
        {a.resolved_at ? `${since(a.resolved_at)} ago` : since(a.opened_at)}
      </td>
      <td className="py-1.5 text-right">
        <span className="inline-flex items-center gap-2 whitespace-nowrap">
          <ErrorText error={resolve.error} />
          <AlertLink alert={a} className="btn sm no-underline" title={cli} />
          {resolvable && (
            <Act
              small
              cli={`dock alerts resolve ${a.id}${flag(scope)}`}
              disabled={resolve.isPending}
              title="Close it by hand; it opens again if the condition is still there"
              onClick={() => resolve.mutate(a.id)}
            >
              Resolve
            </Act>
          )}
        </span>
      </td>
    </tr>
  );
}

function Channels() {
  const scope = useContext(Scope);
  const channels = useAlertChannels(scope);
  const [adding, setAdding] = useState(false);
  return (
    <section aria-labelledby="channels" className="flex flex-col gap-2">
      <SectionHead
        id="channels"
        right={
          !adding && (
            <Button small onClick={() => setAdding(true)}>
              Add channel
            </Button>
          )
        }
      >
        Channels
      </SectionHead>
      {channels.length > 0 ? (
        <Panel>
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Kind</th>
                <th>Target</th>
                <th>Sends</th>
                <th>Reminds</th>
                <th>Last sent</th>
                <th>Failing</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {channels.map((ch) => (
                <ChannelRow key={ch.name} channel={ch} />
              ))}
            </tbody>
          </table>
        </Panel>
      ) : (
        !adding && (
          <p className="py-2 text-mut">
            No channels: alerts only show here and in{" "}
            <span className="font-mono">dock alerts{flag(scope)}</span>.
          </p>
        )
      )}
      {adding && <AddChannel onClose={() => setAdding(false)} />}
    </section>
  );
}

const renotifyText = (m: number) =>
  m === 0 ? "never" : m % 60 === 0 ? `every ${m / 60} h` : `every ${m} min`;

function ChannelRow({ channel: ch }: { channel: AlertChannel }) {
  const scope = useContext(Scope);
  const test = useTestAlertChannel(scope);
  const remove = useRemoveAlertChannel(scope);
  const [confirming, setConfirming] = useState(false);
  const result = test.data;
  return (
    <tr>
      <td className="py-2 font-mono">{ch.name}</td>
      <td className="py-2">{ch.kind}</td>
      <td className="py-2 font-mono">{ch.target}</td>
      <td className="py-2">{ch.min_severity} and up</td>
      <td className="py-2 text-mut">{renotifyText(ch.renotify_minutes)}</td>
      <td className="py-2 text-mut">
        {ch.last_sent_at ? `${since(ch.last_sent_at)} ago` : "never"}
      </td>
      <td className="max-w-[420px] min-w-[160px] py-2 whitespace-normal">
        {ch.failures ? (
          <span className="text-fail">
            {ch.failures} failed
            {ch.last_error && (
              <span className="block text-xs">{ch.last_error}</span>
            )}
          </span>
        ) : (
          <span className="text-mut">no</span>
        )}
      </td>
      <td className="w-full py-1.5 text-right">
        <span className="inline-flex items-center gap-3">
          {result && (
            <span
              className={result.ok ? "text-ok" : "text-fail"}
              title={result.error}
            >
              {result.ok ? "Sent" : "Not sent"}
            </span>
          )}
          <ErrorText error={test.error} />
          <Act
            small
            cli={`dock alerts channel test ${ch.name}${flag(scope)}`}
            disabled={test.isPending}
            onClick={() => test.mutate(ch.name)}
          >
            Send test
          </Act>
          <Button danger small onClick={() => setConfirming(true)}>
            Remove
          </Button>
          {confirming && (
            <Confirm
              danger
              action={`Remove ${ch.name}`}
              cli={`dock alerts channel remove ${ch.name}${flag(scope)}`}
              pending={remove.isPending}
              error={remove.error}
              onConfirm={() =>
                remove.mutate(ch.name, {
                  onSuccess: () => setConfirming(false),
                })
              }
              onCancel={() => {
                remove.reset();
                setConfirming(false);
              }}
            >
              Alerts stop going to <b>{ch.name}</b> ({ch.target}).
            </Confirm>
          )}
        </span>
      </td>
    </tr>
  );
}

type Kind = NewAlertChannel["kind"];

const kinds: Array<{ kind: Kind; note: string }> = [
  { kind: "webhook", note: "JSON, signed with HMAC-SHA256" },
  { kind: "slack", note: "a Slack-compatible incoming webhook" },
  { kind: "email", note: "through Dock's email server" },
];

function AddChannel({ onClose }: { onClose: () => void }) {
  const scope = useContext(Scope);
  const create = useCreateAlertChannel(scope);
  const [name, setName] = useState("");
  const v = useValidation();
  const nameError = v.required(!name.trim(), "Name the channel, like ops.");
  const [kind, setKind] = useState<Kind>("slack");
  const [url, setUrl] = useState("");
  // A webhook's own signing secret; one is generated when left empty.
  const [signing, setSigning] = useState("");
  const [severity, setSeverity] = useState<Severity>("warning");
  const [hours, setHours] = useState("4");
  // An email channel's recipients, comma separated.
  const [to, setTo] = useState("");
  const [secret, setSecret] = useState<string>();
  // Why the new channel can't send yet: email while it isn't set up.
  const [warning, setWarning] = useState<string>();

  if (warning) {
    return (
      <div className="flex max-w-[760px] flex-col gap-2.5 border-t border-line pt-3.5">
        <p role="status" className="text-warn">
          {warning}
        </p>
        <div>
          <Button small onClick={onClose}>
            OK
          </Button>
        </div>
      </div>
    );
  }
  if (secret) {
    return (
      <div className="flex max-w-[760px] flex-col gap-2.5 rounded-panel border border-ok-edge bg-ok-bg/30 p-4">
        <p>
          <b className="font-semibold">Copy the signing secret now.</b>{" "}
          <span className="text-mut">
            It is shown only once. Check each delivery's Dock-Signature header
            with it.
          </span>
        </p>
        <div className="flex flex-wrap items-center gap-2.5 rounded-ctl border border-line bg-side px-3 py-2.5">
          <code className="min-w-0 flex-1 overflow-x-auto font-mono whitespace-nowrap">
            {secret}
          </code>
          <CopyButton text={secret} primary />
        </div>
        <div>
          <Button small onClick={onClose}>
            I've saved it
          </Button>
        </div>
      </div>
    );
  }

  const cli = [
    `dock alerts channel add ${name.trim() || "<name>"}${flag(scope)}`,
    kind === "email"
      ? `--email ${to.replace(/\s/g, "") || "<to>"}`
      : `--${kind} -`,
    kind === "webhook" && signing && "--secret …",
    severity === "critical" && "--min-severity critical",
    hours !== "4" && `--renotify ${Number(hours) || 0}h`,
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <form
      ref={v.form}
      aria-label="Add channel"
      className="flex max-w-[760px] flex-col gap-3.5 border-t border-line pt-3.5"
      onKeyDown={(e) => e.key === "Escape" && onClose()}
      onSubmit={(e) => {
        e.preventDefault();
        if (!v.check(!!name.trim())) return;
        const input: NewAlertChannel = {
          name: name.trim(),
          kind,
          min_severity: severity,
          renotify_minutes: Math.round(Number(hours || 0) * 60),
        };
        if (kind === "email") {
          input.to = to
            .split(",")
            .map((s) => s.trim())
            .filter(Boolean);
        } else {
          input.url = url.trim();
          if (kind === "webhook" && signing) input.secret = signing;
        }
        create.mutate(input, {
          onSuccess: (created) =>
            created.secret
              ? setSecret(created.secret)
              : created.warning
                ? setWarning(created.warning)
                : onClose(),
        });
      }}
    >
      <div className="flex flex-wrap gap-3">
        <label className="flex flex-[1_1_180px] flex-col gap-1.5">
          <span>Name</span>
          <input
            // biome-ignore lint/a11y/noAutofocus: the form opens on request
            autoFocus
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="ops"
            aria-invalid={!!nameError}
            className={`field font-mono ${nameError ? "border-fail" : ""}`}
          />
          {nameError && (
            <span role="alert" className="text-xs text-fail">
              {nameError}
            </span>
          )}
        </label>
        <label className="flex flex-[1_1_180px] flex-col gap-1.5">
          <span>Sends</span>
          <select
            value={severity}
            onChange={(e) => setSeverity(e.target.value as Severity)}
            className="py-[7px] text-sm"
          >
            <option value="warning">warning and critical</option>
            <option value="critical">critical only</option>
          </select>
        </label>
        <label className="flex flex-[1_1_180px] flex-col gap-1.5">
          <span>
            Remind every <span className="text-mut">hours, 0 never</span>
          </span>
          <input
            type="number"
            min={0}
            step="any"
            value={hours}
            onChange={(e) => setHours(e.target.value)}
            className="field font-mono"
          />
        </label>
      </div>
      <fieldset>
        <legend className="mb-1.5">Kind</legend>
        <div className="flex flex-col gap-1.5">
          {kinds.map((k) => (
            <label key={k.kind} className="flex items-baseline gap-2">
              <input
                type="radio"
                name="kind"
                checked={kind === k.kind}
                onChange={() => setKind(k.kind)}
              />
              <span>
                <b className="font-medium">{k.kind}</b>{" "}
                <span className="text-mut">{k.note}</span>
              </span>
            </label>
          ))}
        </div>
      </fieldset>
      {kind === "email" ? (
        <label className="flex flex-col gap-1.5">
          <span>
            To{" "}
            <span className="text-mut">
              comma separated; Dock's email server sends, set up by an operator
            </span>
          </span>
          <input
            value={to}
            onChange={(e) => setTo(e.target.value)}
            placeholder="ops@example.com, oncall@example.com"
            className="field font-mono"
          />
        </label>
      ) : (
        <label className="flex flex-col gap-1.5">
          <span>
            URL <span className="text-mut">stored encrypted, never shown</span>
          </span>
          <input
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://hooks.slack.com/services/…"
            className="field font-mono"
          />
        </label>
      )}
      {kind === "webhook" && (
        <label className="flex flex-col gap-1.5">
          <span>
            Signing secret{" "}
            <span className="text-mut">
              optional, generated and shown once when empty
            </span>
          </span>
          <input
            type="password"
            autoComplete="off"
            value={signing}
            onChange={(e) => setSigning(e.target.value)}
            className="field font-mono"
          />
        </label>
      )}
      <div className="flex flex-wrap items-center gap-2.5">
        <Act primary type="submit" cli={cli} disabled={create.isPending}>
          Add channel
        </Act>
        <Button onClick={onClose}>Cancel</Button>
        <span className="text-xs text-fail">
          {v.problem([!name.trim() && "a name"])}
        </span>
        <ErrorText error={create.error} />
      </div>
    </form>
  );
}
