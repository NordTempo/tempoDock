import { type FormEvent, useState } from "react";
import { day, since, size, until } from "../../ui/format";
import {
  Act,
  Button,
  CopyButton,
  ErrorText,
  Page,
  Panel,
  SectionHead,
  StateText,
  TitleRow,
} from "../../ui/kit";
import { hourLabel } from "./backups";
import {
  type BackupSettings,
  type ConfigureBackup,
  useBackupSettings,
  useConfigureBackups,
  useRevealBackupPassword,
  useRunControlPlaneBackup,
} from "./settings";

// The console's Backups page: where every backup goes, how long it is
// kept, and the control plane's own daily backup. Services' backups are on
// their own pages.

export function BackupSettingsPage() {
  const settings = useBackupSettings();
  const run = useRunControlPlaneBackup();
  const latest = settings.control_plane.latest;
  return (
    <Page>
      <TitleRow
        title="Backups"
        context={
          settings.off_box
            ? `Off-box to ${settings.repository}`
            : "Local repositories, one on each server"
        }
        actions={
          <Act
            cli="dock backup run --control-plane"
            disabled={run.isPending || latest?.status === "running"}
            onClick={() => run.mutate()}
          >
            Back up the control plane now
          </Act>
        }
      />
      {!settings.off_box && (
        <p className="rounded-panel border border-warn-edge bg-warn-bg/60 px-3.5 py-3 text-warn-text">
          Backups stay on the server they were made on, so losing a server loses
          its backups. Set an S3 repository below to send them off-box.
        </p>
      )}
      <ErrorText error={run.error} />
      <ControlPlane settings={settings} />
      <Settings settings={settings} />
      <Password />
    </Page>
  );
}

const runState = {
  running: { tone: "prog", glyph: "◐", label: "running" },
  succeeded: { tone: "ok", glyph: "●", label: "succeeded" },
  failed: { tone: "fail", glyph: "✕", label: "failed" },
} as const;

/** The latest control plane backup and when the next one runs. */
function ControlPlane({ settings }: { settings: BackupSettings }) {
  const { latest, next_at } = settings.control_plane;
  return (
    <section aria-labelledby="control-plane">
      <SectionHead
        id="control-plane"
        aside="its database, keys and config, daily"
        right={next_at && <span>next in {until(next_at)}</span>}
      >
        Control plane
      </SectionHead>
      {latest ? (
        <Panel>
          <table>
            <thead>
              <tr>
                <th>Latest</th>
                <th>Started</th>
                <th>Trigger</th>
                <th>Size</th>
                <th>Snapshot</th>
                <th>Repository</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <td className="py-2">
                  <StateText state={runState[latest.status]} />
                  {latest.error && (
                    <span className="block text-xs text-fail">
                      {latest.error}
                    </span>
                  )}
                </td>
                <td className="py-2" title={day(latest.started_at)}>
                  {since(latest.started_at)} ago
                </td>
                <td className="py-2 text-mut">
                  {latest.trigger} · {latest.created_by}
                </td>
                <td className="py-2 font-mono">
                  {latest.size_bytes ? size(latest.size_bytes) : "·"}
                </td>
                <td className="py-2 font-mono">
                  {latest.snapshot?.slice(0, 8) || "·"}
                </td>
                <td className="w-full py-2 font-mono">
                  {latest.repository || "local"}
                  {!latest.off_box && (
                    <span className="font-sans text-warn"> not off-box</span>
                  )}
                </td>
              </tr>
            </tbody>
          </table>
        </Panel>
      ) : (
        <p className="text-mut">
          Not backed up yet.
          {next_at && ` The first runs in ${until(next_at)}.`}
        </p>
      )}
    </section>
  );
}

const field = "field py-[7px]";

/**
 * Repository, credentials, retention and hour in one form. Only changed
 * fields are sent; blank credentials keep the stored ones.
 */
function Settings({ settings }: { settings: BackupSettings }) {
  const configure = useConfigureBackups();
  const [repository, setRepository] = useState(settings.repository);
  const [keyId, setKeyId] = useState("");
  const [secret, setSecret] = useState("");
  const [region, setRegion] = useState("");
  const [keep, setKeep] = useState({
    daily: settings.keep_daily,
    weekly: settings.keep_weekly,
    monthly: settings.keep_monthly,
  });
  const [hour, setHour] = useState(settings.hour);
  const [password, setPassword] = useState<string>();

  const s3 = repository.trim() !== "";
  const changes: ConfigureBackup = {
    ...(repository.trim() !== settings.repository && {
      repository: repository.trim(),
    }),
    ...(s3 && keyId && { access_key_id: keyId }),
    ...(s3 && secret && { secret_access_key: secret }),
    ...(s3 && region && { region }),
    ...(keep.daily !== settings.keep_daily && { keep_daily: keep.daily }),
    ...(keep.weekly !== settings.keep_weekly && { keep_weekly: keep.weekly }),
    ...(keep.monthly !== settings.keep_monthly && {
      keep_monthly: keep.monthly,
    }),
    ...(hour !== settings.hour && { hour }),
  };
  const dirty = Object.keys(changes).length > 0;

  const save = (e: FormEvent) => {
    e.preventDefault();
    configure.mutate(changes, {
      onSuccess: (saved) => {
        setKeyId("");
        setSecret("");
        setRegion("");
        if (saved.password) setPassword(saved.password);
      },
    });
  };

  const count = (label: string, k: keyof typeof keep, max: number) => (
    <label className="flex flex-[1_1_120px] flex-col gap-1.5">
      <span>{label}</span>
      <input
        type="number"
        min={k === "daily" ? 1 : 0}
        max={max}
        value={keep[k]}
        onChange={(e) => setKeep({ ...keep, [k]: Number(e.target.value) })}
        className={`${field} font-mono`}
      />
    </label>
  );

  return (
    <section aria-labelledby="settings">
      <SectionHead
        id="settings"
        aside={
          settings.updated_by &&
          settings.updated_at &&
          `changed by ${settings.updated_by} ${since(settings.updated_at)} ago`
        }
      >
        Where and how long
      </SectionHead>
      <form
        aria-label="Backup settings"
        className="flex max-w-[760px] flex-col gap-3.5"
        onSubmit={save}
      >
        <label className="flex flex-col gap-1.5">
          <span>
            Repository{" "}
            <span className="text-mut">empty keeps backups on each server</span>
          </span>
          <input
            value={repository}
            onChange={(e) => setRepository(e.target.value)}
            placeholder="s3:https://s3.eu-central-1.amazonaws.com/bucket/dock"
            className={`${field} font-mono`}
          />
        </label>
        {s3 && (
          <div className="flex flex-wrap gap-3">
            <label className="flex flex-[1_1_180px] flex-col gap-1.5">
              <span>Access key ID</span>
              <input
                value={keyId}
                onChange={(e) => setKeyId(e.target.value)}
                placeholder={settings.has_credentials ? "unchanged" : ""}
                autoComplete="off"
                className={`${field} font-mono`}
              />
            </label>
            <label className="flex flex-[1_1_180px] flex-col gap-1.5">
              <span>Secret access key</span>
              <input
                type="password"
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
                placeholder={settings.has_credentials ? "unchanged" : ""}
                autoComplete="new-password"
                className={`${field} font-mono`}
              />
            </label>
            <label className="flex flex-[1_1_120px] flex-col gap-1.5">
              <span>Region</span>
              <input
                value={region}
                onChange={(e) => setRegion(e.target.value)}
                placeholder={settings.has_credentials ? "unchanged" : ""}
                className={`${field} font-mono`}
              />
            </label>
          </div>
        )}
        <div className="flex flex-wrap gap-3">
          {count("Keep daily", "daily", 365)}
          {count("Keep weekly", "weekly", 520)}
          {count("Keep monthly", "monthly", 240)}
          <label className="flex flex-[1_1_120px] flex-col gap-1.5">
            <span>
              Daily at <span className="text-mut">{settings.time_zone}</span>
            </span>
            <select
              value={hour}
              onChange={(e) => setHour(Number(e.target.value))}
              className={field}
            >
              {Array.from({ length: 24 }, (_, h) => (
                <option key={h} value={h}>
                  {hourLabel(h)}
                </option>
              ))}
            </select>
          </label>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <Button
            primary
            type="submit"
            disabled={!dirty || configure.isPending}
          >
            Save
          </Button>
          <span className="font-mono text-2xs text-faint">
            dock backup configure
          </span>
          <ErrorText error={configure.error} />
        </div>
      </form>
      {password && <PasswordBox password={password} first />}
    </section>
  );
}

/** Reveals the repository password, which reading the backups needs. */
function Password() {
  const reveal = useRevealBackupPassword();
  return (
    <section aria-labelledby="password">
      <SectionHead id="password">Repository password</SectionHead>
      {reveal.data ? (
        <PasswordBox password={reveal.data} />
      ) : (
        <div className="flex flex-wrap items-center gap-3">
          <span className="text-mut">
            Without it the backups can't be read after losing this server.
            Revealing it is audited.
          </span>
          <Act
            small
            cli="dock backup status --reveal"
            disabled={reveal.isPending}
            onClick={() => reveal.mutate()}
          >
            Reveal
          </Act>
          <ErrorText error={reveal.error} />
        </div>
      )}
    </section>
  );
}

function PasswordBox({
  password,
  first,
}: {
  password: string;
  first?: boolean;
}) {
  return (
    <div className="mt-3 flex max-w-[760px] flex-col gap-2.5 rounded-panel border border-ok-edge bg-ok-bg/30 p-4">
      {first && (
        <p>
          <b className="font-semibold">Store the repository password now.</b>{" "}
          <span className="text-mut">
            Without it the backups can't be read after losing this server.
          </span>
        </p>
      )}
      <div className="flex flex-wrap items-center gap-2.5 rounded-ctl border border-line bg-side px-3 py-2.5">
        <code className="min-w-0 flex-1 overflow-x-auto font-mono whitespace-nowrap">
          {password}
        </code>
        <CopyButton text={password} primary />
      </div>
    </div>
  );
}
