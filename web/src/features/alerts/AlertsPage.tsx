import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, list } from "../../api/client";
import { ErrorBlock } from "../../ui/ErrorBlock";
import { since } from "../../ui/format";
import {
  Act,
  Button,
  CopyButton,
  ErrorText,
  Glyph,
  Page,
  PageBar,
  Panel,
  SectionHead,
} from "../../ui/kit";
import { toneText } from "../../ui/status";
import {
  type Alert,
  type AlertChannel,
  type NewAlertChannel,
  type Severity,
  useAlertChannels,
  useAllAlerts,
  useCreateAlertChannel,
  useOpenAlerts,
  useRemoveAlertChannel,
  useResolveAlert,
  useTestAlertChannel,
} from "./data";

// The Alerts page: open alerts (worst first), recent history, and the
// channels alerts are sent to. Admins only; the route says so to others.

/** Resolved alerts shown under the open ones, and on request. */
const HISTORY = 50;
const MAX = 500;

/** Up to MAX alerts (`dock alerts --all --limit 500`), fetched on request. */
const useOlderAlerts = (enabled: boolean) =>
  useQuery({
    queryKey: ["alerts", "all", MAX],
    queryFn: () =>
      list(
        api.GET("/api/v1/alerts", {
          params: { query: { all: true, limit: MAX } },
        }),
      ),
    enabled,
  });

export function AlertsPage() {
  const open = useOpenAlerts();
  const recent = useAllAlerts();
  const [more, setMore] = useState(false);
  const older = useOlderAlerts(more);
  const resolved = (older.data ?? recent)
    .filter((a) => a.resolved_at)
    .slice(0, more ? MAX : HISTORY);
  const critical = open.filter((a) => a.severity === "critical").length;
  return (
    <>
      <PageBar
        title="Alerts"
        after={
          <span className="text-xs text-faint">
            {open.length === 0
              ? "nothing open"
              : `${open.length} open${critical ? ` · ${critical} critical` : ""}`}
          </span>
        }
        actions={
          <span className="font-mono text-2xs text-mut">dock alerts</span>
        }
      />
      <Page>
        {open.length > 0 && (
          <section aria-labelledby="open">
            <SectionHead id="open">Open</SectionHead>
            <AlertTable alerts={open} resolvable />
          </section>
        )}
        <section aria-labelledby="history">
          <SectionHead
            id="history"
            aside={`last ${more ? MAX : HISTORY}`}
            right={
              !more &&
              resolved.length === HISTORY && (
                <Act
                  small
                  cli={`dock alerts --all --limit ${MAX}`}
                  onClick={() => setMore(true)}
                >
                  Show up to {MAX}
                </Act>
              )
            }
          >
            Resolved
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
        <Channels />
      </Page>
    </>
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
            <th>Where</th>
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
  const tone = a.resolved_at ? "text-mut" : toneText[severityTone[a.severity]];
  return (
    <tr>
      <td className={`py-2 whitespace-nowrap ${tone}`}>
        <Glyph char={a.severity === "critical" ? "✕" : "▲"} /> {a.severity}
      </td>
      <td className="py-2">
        {a.title}
        {a.message && <span className="block text-mut">{a.message}</span>}
      </td>
      <td className="py-2 font-mono">{where(a)}</td>
      <td className="py-2 whitespace-nowrap text-mut">
        {a.resolved_at ? `${since(a.resolved_at)} ago` : since(a.opened_at)}
      </td>
      <td className="w-full py-1.5 text-right">
        {resolvable && (
          <span className="inline-flex items-center gap-3">
            <ErrorText error={resolve.error} />
            <Act
              small
              cli={`dock alerts resolve ${a.id}`}
              disabled={resolve.isPending}
              title="Close it by hand; it opens again if the condition is still there"
              onClick={() => resolve.mutate(a.id)}
            >
              Resolve
            </Act>
          </span>
        )}
      </td>
    </tr>
  );
}

function Channels() {
  const channels = useAlertChannels();
  const [adding, setAdding] = useState(false);
  return (
    <section aria-labelledby="channels" className="flex flex-col gap-2">
      <SectionHead
        id="channels"
        aside="where alerts are sent"
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
            <span className="font-mono">dock alerts</span>.
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
  const test = useTestAlertChannel();
  const remove = useRemoveAlertChannel();
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
      <td className="max-w-[420px] min-w-[280px] py-2 whitespace-normal">
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
          <ErrorText error={test.error ?? remove.error} />
          <Act
            small
            cli={`dock alerts channel test ${ch.name}`}
            disabled={test.isPending}
            onClick={() => test.mutate(ch.name)}
          >
            Send test
          </Act>
          {confirming ? (
            <>
              <Act
                small
                danger
                cli={`dock alerts channel remove ${ch.name}`}
                disabled={remove.isPending}
                onClick={() => remove.mutate(ch.name)}
              >
                Remove now
              </Act>
              <Button small onClick={() => setConfirming(false)}>
                Cancel
              </Button>
            </>
          ) : (
            <Button small onClick={() => setConfirming(true)}>
              Remove
            </Button>
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
  { kind: "email", note: "through an SMTP server" },
];

function AddChannel({ onClose }: { onClose: () => void }) {
  const create = useCreateAlertChannel();
  const [name, setName] = useState("");
  const [kind, setKind] = useState<Kind>("slack");
  const [url, setUrl] = useState("");
  // A webhook's own signing secret; one is generated when left empty.
  const [signing, setSigning] = useState("");
  const [severity, setSeverity] = useState<Severity>("warning");
  const [hours, setHours] = useState("4");
  const [smtp, setSmtp] = useState({
    host: "",
    port: "587",
    username: "",
    password: "",
    from: "",
    to: "",
  });
  const [secret, setSecret] = useState<string>();
  const set = (k: keyof typeof smtp) => (e: { target: { value: string } }) =>
    setSmtp((s) => ({ ...s, [k]: e.target.value }));

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
    `dock alerts channel add ${name.trim() || "<name>"}`,
    kind === "email"
      ? [
          `--email ${smtp.to.replace(/\s/g, "") || "<to>"}`,
          `--from ${smtp.from.trim() || "<address>"}`,
          `--smtp-host ${smtp.host.trim() || "<host>"}`,
          smtp.port !== "587" && `--smtp-port ${smtp.port}`,
          smtp.username && `--smtp-user ${smtp.username} --smtp-password-stdin`,
        ]
          .filter(Boolean)
          .join(" ")
      : `--${kind} -`,
    kind === "webhook" && signing && "--secret …",
    severity === "critical" && "--min-severity critical",
    hours !== "4" && `--renotify ${Number(hours) || 0}h`,
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <form
      aria-label="Add channel"
      className="flex max-w-[760px] flex-col gap-3.5 border-t border-line pt-3.5"
      onKeyDown={(e) => e.key === "Escape" && onClose()}
      onSubmit={(e) => {
        e.preventDefault();
        const input: NewAlertChannel = {
          name: name.trim(),
          kind,
          min_severity: severity,
          renotify_minutes: Math.round(Number(hours || 0) * 60),
        };
        if (kind === "email") {
          input.smtp = {
            host: smtp.host.trim(),
            port: Number(smtp.port) || 587,
            from: smtp.from.trim(),
            to: smtp.to
              .split(",")
              .map((s) => s.trim())
              .filter(Boolean),
            ...(smtp.username && {
              username: smtp.username,
              password: smtp.password,
            }),
          };
        } else {
          input.url = url.trim();
          if (kind === "webhook" && signing) input.secret = signing;
        }
        create.mutate(input, {
          onSuccess: (created) =>
            created.secret ? setSecret(created.secret) : onClose(),
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
            className="field py-[7px] font-mono"
          />
        </label>
        <label className="flex flex-[1_1_180px] flex-col gap-1.5">
          <span>Sends</span>
          <select
            value={severity}
            onChange={(e) => setSeverity(e.target.value as Severity)}
            className="field py-[7px]"
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
            className="field py-[7px] font-mono"
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
        <div className="grid grid-cols-[repeat(auto-fill,minmax(220px,1fr))] gap-3">
          {(
            [
              ["to", "To", "ops@example.com, comma separated"],
              ["from", "From", "dock@example.com"],
              ["host", "SMTP host", "smtp.example.com"],
              ["port", "Port", "587; 465 for TLS"],
              ["username", "User", "optional"],
              ["password", "Password", "stored encrypted"],
            ] as const
          ).map(([k, label, hint]) => (
            <label key={k} className="flex flex-col gap-1.5">
              <span>
                {label} <span className="text-mut">{hint}</span>
              </span>
              <input
                value={smtp[k]}
                onChange={set(k)}
                type={k === "password" ? "password" : "text"}
                className="field py-[7px] font-mono"
              />
            </label>
          ))}
        </div>
      ) : (
        <label className="flex flex-col gap-1.5">
          <span>
            URL <span className="text-mut">stored encrypted, never shown</span>
          </span>
          <input
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://hooks.slack.com/services/…"
            className="field py-[7px] font-mono"
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
            className="field py-[7px] font-mono"
          />
        </label>
      )}
      <div className="flex flex-wrap items-center gap-2.5">
        <Act
          primary
          type="submit"
          cli={cli}
          disabled={create.isPending || !name.trim()}
        >
          Add channel
        </Act>
        <Button onClick={onClose}>Cancel</Button>
        <ErrorText error={create.error} />
      </div>
    </form>
  );
}
