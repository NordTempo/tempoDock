import { useSuspenseQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useServers } from "../../data/queries";
import { bytes } from "../../ui/format";
import {
  Act,
  Confirm,
  ErrorText,
  Fact,
  Facts,
  Page,
  SectionHead,
  TitleRow,
} from "../../ui/kit";
import {
  analyticsDefaultsQuery,
  analyticsStatusQuery,
  useSetAnalyticsServer,
  useSetDefaults,
} from "./data";
import { fields, flags, text, validDays, value } from "./settings";

// The console's Analytics page (ADR 0037), for operators: the analytics
// store's state, the server it runs on, and every site's default settings.

export function AnalyticsConsole() {
  const { data: status } = useSuspenseQuery(analyticsStatusQuery());
  return (
    <Page>
      <TitleRow
        title="Analytics"
        context="cookieless visits, counted per site"
      />
      <Facts>
        <Fact label="Analytics">{status.enabled ? "on" : "off"}</Fact>
        <Fact
          label="Server"
          note={
            status.server_online ? (
              "online"
            ) : (
              <span className="text-fail">offline</span>
            )
          }
        >
          {status.server}
        </Fact>
        <Fact
          label="Store"
          note={status.store_version && `version ${status.store_version}`}
        >
          {status.store_running ? (
            "running"
          ) : status.enabled ? (
            <span className="text-warn-text">not answering</span>
          ) : (
            "not started"
          )}
        </Fact>
        <Fact
          label="Events"
          note={status.store_running ? bytes(status.store_bytes) : undefined}
        >
          {status.events.toLocaleString("en-US")}
        </Fact>
        <Fact label="Locations" note="IP Geolocation by DB-IP, CC BY 4.0">
          {status.geoip || "not downloaded"}
        </Fact>
      </Facts>
      {!status.enabled && (
        <p className="text-mut">
          The store starts when the first hostname is counted: on a service's
          Analytics tab, or with <code>dock analytics enable</code>.
        </p>
      )}
      <ServerPicker current={status.server} />
      <Defaults />
    </Page>
  );
}

/** Moves the store to another server, after saying what that means. */
function ServerPicker({ current }: { current: string }) {
  const servers = useServers();
  const move = useSetAnalyticsServer();
  const [server, setServer] = useState(current);
  const [confirming, setConfirming] = useState(false);
  return (
    <section aria-labelledby="server" className="flex flex-col gap-2">
      <SectionHead
        id="server"
        aside="other servers keep their events until it has them"
      >
        Analytics server
      </SectionHead>
      <span className="flex items-center gap-2.5">
        <select
          aria-label="Analytics server"
          value={server}
          onChange={(e) => setServer(e.target.value)}
        >
          {servers.map((s) => (
            <option key={s.name} value={s.name}>
              {s.name}
            </option>
          ))}
        </select>
        <Act
          cli={`dock analytics server ${server}`}
          disabled={server === current || move.isPending}
          onClick={() => setConfirming(true)}
        >
          Move the store
        </Act>
      </span>
      {confirming && (
        <Confirm
          action={`Move to ${server}`}
          cli={`dock analytics server ${server}`}
          pending={move.isPending}
          error={move.error}
          onConfirm={() =>
            move.mutate(server, { onSuccess: () => setConfirming(false) })
          }
          onCancel={() => setConfirming(false)}
        >
          The store on {current} stops and its data stays on that server's
          volume. The store on {server} starts empty; events not yet stored wait
          on each server and go to it.
        </Confirm>
      )}
    </section>
  );
}

/** Every site's defaults: what a site uses where it sets nothing. */
function Defaults() {
  const { data: defaults } = useSuspenseQuery(analyticsDefaultsQuery());
  const set = useSetDefaults();
  // What the form started from: a refetch (another operator's change)
  // doesn't make untouched fields look changed, or save them back.
  const [base, setBase] = useState(() =>
    Object.fromEntries(fields.map((f) => [f.key, text(defaults, f.key)])),
  );
  const [draft, setDraft] = useState(base);
  const changed = fields.filter((f) => draft[f.key] !== base[f.key]);
  const valid = validDays(draft.event_retention_days ?? "");
  return (
    <section aria-labelledby="defaults" className="flex flex-col gap-2">
      <SectionHead id="defaults" aside="sites use these where they set nothing">
        Site defaults
      </SectionHead>
      <form
        className="flex max-w-xl flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          set.mutate(
            {
              settings: Object.assign(
                {},
                ...changed.map((f) => value(f.key, draft[f.key] ?? "")),
              ),
            },
            { onSuccess: () => setBase(draft) },
          );
        }}
      >
        {fields.map(({ key, label, help, list }) => (
          <div key={key} className="flex flex-col gap-1">
            <label htmlFor={`default-${key}`} className="text-sm font-semibold">
              {label}
            </label>
            {list ? (
              <textarea
                id={`default-${key}`}
                rows={3}
                className="font-mono"
                value={draft[key]}
                onChange={(e) =>
                  setDraft((d) => ({ ...d, [key]: e.target.value }))
                }
              />
            ) : (
              <input
                id={`default-${key}`}
                type="number"
                min={1}
                max={3650}
                className="w-28"
                value={draft[key]}
                onChange={(e) =>
                  setDraft((d) => ({ ...d, [key]: e.target.value }))
                }
              />
            )}
            <span className="text-xs text-faint">{help}</span>
          </div>
        ))}
        <ErrorText error={set.error} />
        <span>
          <Act
            primary
            type="submit"
            cli={`dock analytics defaults set${changed
              .flatMap((f) => flags(f.key, draft[f.key] ?? ""))
              .map((f) => ` ${f}`)
              .join("")}`}
            disabled={!changed.length || !valid || set.isPending}
          >
            {set.isPending ? "Saving…" : "Save defaults"}
          </Act>
        </span>
      </form>
    </section>
  );
}
