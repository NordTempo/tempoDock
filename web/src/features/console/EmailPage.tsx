import { useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type ReactNode, useId, useState } from "react";
import { useShortcuts } from "../../shell/keys";
import { clock, day, plural } from "../../ui/format";
import {
  Act,
  Confirm,
  ErrorText,
  Glyph,
  Page,
  PageBar,
  Panel,
} from "../../ui/kit";
import {
  type EmailDelivery,
  type EmailSettings,
  emailQuery,
  outboxQuery,
  useRemoveEmail,
  useSetEmail,
  useTestEmail,
} from "./email";

// The console's Email page (ADR 0079, `dock email`): a strip that says
// whether mail goes out, the SMTP server and sender as one form, and the
// latest mail. Chosen layout A of the 2026-11-13 mocks.

type Security = NonNullable<EmailSettings["security"]>;

export function EmailPage() {
  const st = useSuspenseQuery(emailQuery()).data;
  const test = useTestEmail();
  const [turningOff, setTurningOff] = useState(false);
  useShortcuts(
    st.configured
      ? [{ key: "t", label: "send a test email", run: () => test.mutate() }]
      : [],
  );
  return (
    <>
      <PageBar
        title="Email"
        actions={
          <>
            {st.configured && (
              <Act
                cli="dock email test"
                disabled={test.isPending}
                onClick={() => test.mutate()}
              >
                Send test <kbd>T</kbd>
              </Act>
            )}
            <button type="submit" form="email-settings" className="btn pri">
              Save
            </button>
          </>
        }
      />
      <Page gap="gap-0">
        <Strip settings={st} />
        {test.data && !test.data.ok && (
          <p className="pt-3 text-fail">
            The test email to {test.data.to} failed: {test.data.error}
          </p>
        )}
        <ErrorText error={test.error} />
        <SettingsForm key={st.updated_at ?? "new"} settings={st} />
        {st.configured && <RecentMail />}
        {st.configured && (
          <div className="flex items-center gap-2 pt-4">
            <Act
              small
              danger
              cli="dock email off"
              onClick={() => setTurningOff(true)}
            >
              Turn off email
            </Act>
          </div>
        )}
        {turningOff && (
          <TurnOff
            queued={st.status.queued}
            onDone={() => setTurningOff(false)}
          />
        )}
      </Page>
    </>
  );
}

/** Whether mail goes out, from the latest attempt, and the queue. */
function Strip({ settings: st }: { settings: EmailSettings }) {
  const s = st.status;
  const failing =
    !!s.last_error_at &&
    (!s.last_ok_at || Date.parse(s.last_error_at) > Date.parse(s.last_ok_at));
  let state: ReactNode;
  if (!st.configured) {
    state = (
      <span>
        <span className="mr-1 text-mut">
          <Glyph char="○" />
        </span>{" "}
        Not set up. Dock sends invites and other mail once a server is set here.
      </span>
    );
  } else if (failing && s.last_error_at) {
    state = (
      <span className="text-fail">
        <span className="mr-1">
          <Glyph char="●" />
        </span>{" "}
        Failing since {when(s.last_error_at)}: {s.last_error}. Check the server
        and login below.
      </span>
    );
  } else if (s.last_ok_at) {
    state = (
      <span>
        <span className="mr-1 text-ok">
          <Glyph char="●" />
        </span>{" "}
        Working. Last mail sent {when(s.last_ok_at)}
      </span>
    );
  } else {
    state = (
      <span>
        <span className="mr-1 text-mut">
          <Glyph char="○" />
        </span>{" "}
        Nothing sent yet. Send a test to check the settings.
      </span>
    );
  }
  return (
    <div className="flex flex-wrap items-center gap-x-7 gap-y-2 border-b border-line pb-3.5">
      {state}
      {st.configured && (
        <>
          <span className="text-mut">
            {s.queued} waiting
            {s.queued > 0 && s.next_at && `, next try ${clock(s.next_at)}`}
          </span>
          <span className="text-mut">{s.failed_day} failed today</span>
        </>
      )}
      {/* The CLI that does the same, at the strip's far end. */}
      <code className="ml-auto font-mono text-2xs text-faint">dock email</code>
    </div>
  );
}

/** The server and the sender, saved together. */
function SettingsForm({ settings: st }: { settings: EmailSettings }) {
  const save = useSetEmail();
  const [host, setHost] = useState(st.host ?? "");
  const [port, setPort] = useState(st.port ? String(st.port) : "");
  const [security, setSecurity] = useState<Security>(st.security ?? "starttls");
  const [username, setUsername] = useState(st.username ?? "");
  const [password, setPassword] = useState("");
  const [fromName, setFromName] = useState(st.from_name ?? "");
  const [from, setFrom] = useState(st.from ?? "");
  const [replyTo, setReplyTo] = useState(st.reply_to ?? "");
  const portNumber = port.trim() === "" ? 0 : Number(port);
  const ready =
    !!host.trim() &&
    !!from.trim() &&
    Number.isInteger(portNumber) &&
    portNumber >= 0 &&
    portNumber <= 65535;
  const defaultPort = security === "tls" ? "465" : "587";
  return (
    <form
      id="email-settings"
      aria-label="Email settings"
      className="flex flex-col"
      onSubmit={(e) => {
        e.preventDefault();
        if (!ready || save.isPending) return;
        save.mutate(
          {
            host: host.trim(),
            port: portNumber,
            security,
            username: username.trim(),
            ...(password && { password }),
            from: from.trim(),
            from_name: fromName.trim(),
            reply_to: replyTo.trim(),
          },
          { onSuccess: () => setPassword("") },
        );
      }}
    >
      <section className="flex flex-col gap-3 border-b border-line py-4">
        <h2 className="font-semibold">Server</h2>
        <div className="grid gap-3 sm:grid-cols-[minmax(0,2fr)_90px_minmax(0,1.4fr)]">
          <Field label="Host">
            {(id) => (
              <input
                id={id}
                value={host}
                onChange={(e) => setHost(e.target.value)}
                placeholder="smtp.example.com"
                className="field font-mono"
                required
              />
            )}
          </Field>
          <Field label="Port">
            {(id) => (
              <input
                id={id}
                value={port}
                inputMode="numeric"
                onChange={(e) => setPort(e.target.value)}
                placeholder={defaultPort}
                className="field font-mono"
              />
            )}
          </Field>
          <div className="flex min-w-0 flex-col gap-1.5">
            <span className="text-xs text-mut">Security</span>
            <div
              role="radiogroup"
              aria-label="Security"
              className="inline-flex self-start overflow-hidden rounded-ctl border border-field"
            >
              {(
                [
                  ["starttls", "STARTTLS"],
                  ["tls", "TLS"],
                ] as const
              ).map(([value, label]) => (
                <label
                  key={value}
                  className={`cursor-pointer px-3 py-[7px] has-focus-visible:outline ${security === value ? "bg-sel text-fg" : "text-mut hover:text-fg"}`}
                >
                  <input
                    type="radio"
                    name="security"
                    className="sr-only"
                    checked={security === value}
                    onChange={() => setSecurity(value)}
                  />
                  {label}
                </label>
              ))}
            </div>
          </div>
        </div>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Username">
            {(id) => (
              <input
                id={id}
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                placeholder="none"
                autoComplete="off"
                className="field font-mono"
              />
            )}
          </Field>
          <Field label="Password">
            {(id) => (
              <input
                id={id}
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder={st.password_set ? "Set. Type to replace" : "none"}
                autoComplete="new-password"
                disabled={!username.trim()}
                className="field font-mono"
              />
            )}
          </Field>
        </div>
      </section>
      <section className="flex flex-col gap-3 py-4">
        <h2 className="font-semibold">Sender</h2>
        <div className="grid gap-3 sm:grid-cols-3">
          <Field label="From name">
            {(id) => (
              <input
                id={id}
                value={fromName}
                maxLength={64}
                onChange={(e) => setFromName(e.target.value)}
                placeholder="Tempo Dock"
                className="field"
              />
            )}
          </Field>
          <Field label="From address">
            {(id) => (
              <input
                id={id}
                type="email"
                value={from}
                onChange={(e) => setFrom(e.target.value)}
                placeholder="dock@example.com"
                className="field font-mono"
                required
              />
            )}
          </Field>
          <Field label="Reply-To">
            {(id) => (
              <input
                id={id}
                type="email"
                value={replyTo}
                onChange={(e) => setReplyTo(e.target.value)}
                placeholder="none"
                className="field font-mono"
              />
            )}
          </Field>
        </div>
        {st.links_url ? (
          <p className="text-xs text-faint">
            Links in mail open at{" "}
            <span className="font-mono">{st.links_url}</span> (
            <Link to="/console/address" className="text-inherit underline">
              Address
            </Link>
            ).
          </p>
        ) : (
          <p className="text-warn-text">
            <span className="text-warn">▲</span> Dock has no public address, so
            invites aren't emailed: their links would only open here.{" "}
            <Link to="/console/address" className="text-warn-text underline">
              Set a public address
            </Link>
          </p>
        )}
        <ErrorText error={save.error} />
      </section>
    </form>
  );
}

/** A labelled control: `children` gets the id its input takes. */
function Field({
  label,
  children,
}: {
  label: string;
  children: (id: string) => ReactNode;
}) {
  const id = useId();
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <label htmlFor={id} className="text-xs text-mut">
        {label}
      </label>
      {children(id)}
    </div>
  );
}

/** The latest mail, patched live from `email` events. */
function RecentMail() {
  const deliveries = useSuspenseQuery(outboxQuery()).data.deliveries ?? [];
  return (
    <section className="flex flex-col gap-3 py-4">
      <h2 className="font-semibold">Recent mail</h2>
      {deliveries.length === 0 ? (
        <p className="text-mut">No mail yet.</p>
      ) : (
        <Panel>
          <table>
            <thead>
              <tr>
                <th>When</th>
                <th>What</th>
                <th>To</th>
                <th>Org</th>
                <th>Result</th>
              </tr>
            </thead>
            <tbody>
              {deliveries.map((d) => (
                <tr key={d.id}>
                  <td className="font-mono text-xs whitespace-nowrap text-mut">
                    {when(d.created_at)}
                  </td>
                  <td>{kindName[d.kind]}</td>
                  <td className="font-mono text-xs">{d.to}</td>
                  <td className={d.org ? "" : "text-faint"}>
                    {d.org || "platform"}
                  </td>
                  <td>
                    <Result delivery={d} />
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

const kindName: Record<EmailDelivery["kind"], string> = {
  invite: "Invite",
  test: "Test",
  password_reset: "Password reset",
  security: "Security notice",
  alert: "Alert",
  access_request: "Access request",
};

/** A mail's outcome. */
function Result({ delivery: d }: { delivery: EmailDelivery }) {
  if (d.status === "sent") return <span className="text-ok">sent</span>;
  if (d.status === "failed")
    return <span className="text-fail">failed: {d.error}</span>;
  return (
    <span className="text-warn-text">
      {d.error
        ? `retrying${d.next_at ? ` at ${clock(d.next_at)}` : ""}: ${d.error}`
        : "sending"}
    </span>
  );
}

/** Asks before turning email off. */
function TurnOff({ queued, onDone }: { queued: number; onDone: () => void }) {
  const remove = useRemoveEmail();
  return (
    <Confirm
      danger
      action="Turn off email"
      cli="dock email off"
      pending={remove.isPending}
      error={remove.error}
      onConfirm={() => remove.mutate(undefined, { onSuccess: onDone })}
      onCancel={onDone}
    >
      Dock stops sending mail; invites show their link to copy instead.
      {queued > 0 && ` The ${plural(queued, "mail")} waiting won't be sent.`}{" "}
      The settings, password included, are removed.
    </Confirm>
  );
}

/** "14:02" today, "Nov 12 14:02" before. */
function when(iso: string) {
  const sameDay = new Date(iso).toDateString() === new Date().toDateString();
  return sameDay ? clock(iso) : `${day(iso)} ${clock(iso)}`;
}
