import { useQuery, useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useServers } from "../../data/queries";
import { CliHint } from "../../ui/CliHint";
import { ErrorBlock } from "../../ui/ErrorBlock";
import { bytes } from "../../ui/format";
import {
  Act,
  Button,
  Confirm,
  ErrorText,
  Fact,
  Facts,
  Page,
  PageBar,
  SectionHead,
} from "../../ui/kit";
import { Saved } from "../create/form";
import {
  analyticsDefaultsQuery,
  analyticsStatusQuery,
  orgAnalyticsDefaultsQuery,
  useSetAnalyticsServer,
  useSetDefaults,
  useSetOrgDefaults,
} from "./data";
import { SettingInput } from "./SettingInput";
import { type Field, fields, flags, text, valid, value } from "./settings";

// The console's Analytics page (ADR 0037), for operators: the analytics
// store's state, the server it runs on, and every site's default settings.

export function AnalyticsConsole() {
  const { data: status, refetch } = useSuspenseQuery(analyticsStatusQuery());
  return (
    <>
      <PageBar title="Analytics" />
      <Page>
        <Facts>
          <Fact label="Analytics">{status.enabled ? "on" : "off"}</Fact>
          <Fact
            label="Server"
            note={
              // Off, nothing runs there yet: its health isn't the store's.
              !status.enabled ? undefined : status.server_online ? (
                "online"
              ) : (
                <Link
                  to="/console/servers/$server"
                  params={{ server: status.server }}
                  className="text-fail"
                >
                  offline, the store can't run
                </Link>
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
            ) : status.starting ? (
              "starting"
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
          <Fact
            label="Visitor locations"
            note={
              status.geoip
                ? "DB-IP's database, updated monthly"
                : status.enabled
                  ? "Visitor countries show as unknown until Dock has downloaded a free location database. It tries again daily."
                  : "downloaded when the store starts"
            }
          >
            {status.geoip || "none yet"}
          </Fact>
        </Facts>
        {status.moving_to && (
          <p>
            Moving to {status.moving_to} with its data. Events wait on each
            server meanwhile.
          </p>
        )}
        {status.error && (
          <ErrorBlock
            error={new Error(status.error)}
            onRetry={() => refetch()}
          />
        )}
        {!status.enabled && (
          <p className="text-mut">
            The store starts when the first hostname is counted: on a service's
            Analytics tab, or with <code>dock analytics enable</code>.
          </p>
        )}
        <ServerPicker
          current={status.server}
          online={status.server_online}
          moving={!!status.moving_to}
        />
        <Defaults />
        <p className="text-2xs text-faint">
          IP Geolocation by DB-IP, CC BY 4.0
        </p>
      </Page>
    </>
  );
}

/**
 * Where the store runs, as a select that acts on change: picking another
 * server asks first (its data moves along, unless its server is offline
 * and the new store starts empty), then says Saved.
 */
function ServerPicker({
  current,
  online,
  moving,
}: {
  current: string;
  online: boolean;
  moving: boolean;
}) {
  const servers = useServers();
  const move = useSetAnalyticsServer();
  // The server picked and waiting for the confirm.
  const [server, setServer] = useState<string>();
  const cli = `dock analytics server ${server ?? "<server>"}${online ? "" : " --start-empty"}`;
  return (
    <section aria-labelledby="server" className="flex flex-col gap-2">
      <SectionHead id="server">Analytics server</SectionHead>
      <span className="flex flex-wrap items-center gap-2.5">
        <CliHint cli={cli} className="act">
          <select
            aria-label="Analytics server"
            value={server ?? current}
            disabled={moving || move.isPending}
            onChange={(e) => {
              move.reset();
              if (e.target.value !== current) setServer(e.target.value);
            }}
          >
            {servers.map((s) => (
              <option key={s.name} value={s.name}>
                {s.name}
              </option>
            ))}
          </select>
        </CliHint>
        <Saved ok={move.isSuccess} />
        <span className="text-xs text-mut">
          {moving
            ? "A move is running; pick again when it's done."
            : "Pick another server to move the store there, with its data."}
        </span>
      </span>
      {server && (
        <Confirm
          action={`Move to ${server}`}
          cli={cli}
          pending={move.isPending}
          error={move.error}
          onConfirm={() =>
            move.mutate(
              { server, start_empty: !online },
              { onSuccess: () => setServer(undefined) },
            )
          }
          onCancel={() => {
            move.reset();
            setServer(undefined);
          }}
        >
          {online
            ? `Its data is copied to ${server} and checked, then the store and its data are removed from ${current}. Events wait on each server meanwhile.`
            : `${current} is offline, so its data can't be copied: the store on ${server} starts empty, and the old one is removed when ${current} is back.`}
        </Confirm>
      )}
    </section>
  );
}

/** Whose site defaults a form edits: the platform's or the org's. */
export type DefaultsScope = "platform" | "org";

/**
 * Site defaults: what a site uses where it sets nothing. The platform's
 * (operators, the console) apply to every site; an org's (its settings)
 * to the org's new sites, over the platform's. Each setting saves on its
 * own, in the app's save model.
 */
export function SiteDefaults({ scope }: { scope: DefaultsScope }) {
  const platform = useQuery({
    ...analyticsDefaultsQuery(),
    enabled: scope === "platform",
  }).data;
  const org = useQuery({
    ...orgAnalyticsDefaultsQuery(),
    enabled: scope === "org",
  }).data;
  const defaults = scope === "platform" ? platform : org?.effective;
  if (!defaults) return null;
  return (
    <div className="flex max-w-xl flex-col gap-4">
      {fields.map((field) => (
        <DefaultField
          key={field.key}
          scope={scope}
          field={field}
          saved={text(defaults, field.key)}
        />
      ))}
    </div>
  );
}

/** The platform's site defaults, on the console's Analytics page. */
function Defaults() {
  return (
    <section aria-labelledby="defaults" className="flex flex-col gap-2">
      <SectionHead id="defaults">Site defaults</SectionHead>
      <p className="max-w-xl text-xs text-mut">
        What every site uses where neither it nor its organization set
        something. Organizations set their own defaults for new sites in their
        settings.
      </p>
      <SiteDefaults scope="platform" />
    </section>
  );
}

/**
 * One default. A switch saves on change; text and numbers get Save and
 * Cancel while changed (Enter saves a number, Esc cancels). "Saved" after.
 */
function DefaultField({
  scope,
  field,
  saved,
}: {
  scope: DefaultsScope;
  field: Field;
  saved: string;
}) {
  const platformSet = useSetDefaults();
  const orgSet = useSetOrgDefaults();
  const set = scope === "platform" ? platformSet : orgSet;
  const [draft, setDraft] = useState(saved);
  // Another operator's change shows unless this field is being edited.
  const [was, setWas] = useState(saved);
  if (was !== saved) {
    setWas(saved);
    if (draft === was) setDraft(saved);
  }
  const changed = draft !== saved;
  const ok = valid(field.key, draft);
  const cli =
    scope === "platform"
      ? `dock analytics defaults set ${flags(field.key, draft).join(" ")}`
      : undefined;
  const save = (t: string) => set.mutate({ settings: value(field.key, t) });
  const cancel = () => {
    set.reset();
    setDraft(saved);
  };
  const id = `default-${field.key}`;
  return (
    <form
      className="flex flex-col gap-1"
      onSubmit={(e) => {
        e.preventDefault();
        if (changed && ok) save(draft);
      }}
      onKeyDown={(e) => e.key === "Escape" && changed && cancel()}
    >
      <label htmlFor={id} className="text-sm font-semibold">
        {field.label}
      </label>
      <span className="flex flex-wrap items-center gap-2">
        <SettingInput
          id={id}
          field={field}
          value={draft}
          onChange={(t) => {
            set.reset();
            setDraft(t);
            if (field.kind === "switch") save(t);
          }}
        />
        {changed && field.kind !== "switch" && (
          <>
            <Act
              small
              primary
              type="submit"
              cli={cli}
              disabled={!ok || set.isPending}
            >
              Save
            </Act>
            <Button small onClick={cancel}>
              Cancel
            </Button>
          </>
        )}
        <Saved ok={set.isSuccess && !changed} />
        <ErrorText error={set.error} />
      </span>
      <span className="text-xs text-mut">{field.help}</span>
    </form>
  );
}
