import { type FormEvent, useState } from "react";
import { day, since, size, until } from "../../ui/format";
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
  StateText,
} from "../../ui/kit";
import { BackupStorage } from "./BackupStorage";
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
// kept, and the control plane's own daily backup (its database, keys and
// config). Services' backups are on
// their own pages.

export function BackupSettingsPage() {
  const settings = useBackupSettings();
  const run = useRunControlPlaneBackup();
  const [moving, setMoving] = useState(false);
  const latest = settings.control_plane.latest;
  const busy = settings.move?.running;
  return (
    <>
      <PageBar
        title="Backups"
        after={
          <span className="text-xs text-faint">
            {busy
              ? `moving to ${settings.move?.to}`
              : settings.storage
                ? `off-box in ${settings.storage}, folder ${settings.path}`
                : settings.off_box
                  ? `off-box to ${settings.repository}`
                  : "local repositories, one on each server"}
          </span>
        }
        actions={
          <>
            {!moving && (
              <Button disabled={busy} onClick={() => setMoving(true)}>
                Move backups
              </Button>
            )}
            <Act
              cli="dock backup run --control-plane"
              disabled={run.isPending || latest?.status === "running" || busy}
              onClick={() => run.mutate()}
            >
              Back up the control plane now
            </Act>
          </>
        }
      />
      <Page>
        {!settings.off_box && (
          <p className="text-warn-text">
            <span className="text-warn">
              <Glyph char="▲" />
            </span>{" "}
            Backups stay on the server they were made on, so losing a server
            loses its backups.{" "}
            {!moving && !busy && (
              <button
                type="button"
                className="text-fg underline underline-offset-3"
                onClick={() => setMoving(true)}
              >
                Move to a bucket
              </button>
            )}
          </p>
        )}
        <ErrorText error={run.error} />
        <BackupStorage
          settings={settings}
          moving={moving}
          onMoved={() => setMoving(false)}
        />
        <ControlPlane settings={settings} />
        <Settings settings={settings} />
        <Password />
      </Page>
    </>
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

/** Retention and the hour in one form; only changed fields are sent. */
function Settings({ settings }: { settings: BackupSettings }) {
  const configure = useConfigureBackups();
  const [keep, setKeep] = useState({
    daily: settings.keep_daily,
    weekly: settings.keep_weekly,
    monthly: settings.keep_monthly,
  });
  const [hour, setHour] = useState(settings.hour);
  const [password, setPassword] = useState<string>();

  const changes: ConfigureBackup = {
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
        className="field py-[7px] font-mono"
      />
    </label>
  );

  return (
    <section aria-labelledby="settings">
      <SectionHead
        id="settings"
        right={
          settings.updated_by &&
          settings.updated_at &&
          `changed by ${settings.updated_by} ${since(settings.updated_at)} ago`
        }
      >
        Schedule
      </SectionHead>
      <form
        aria-label="Backup settings"
        className="flex max-w-[760px] flex-col gap-3.5"
        onSubmit={save}
      >
        <div className="flex flex-wrap gap-3">
          {count("Keep daily", "daily", 365)}
          {count("Keep weekly", "weekly", 520)}
          {count("Keep monthly", "monthly", 240)}
          <label className="flex flex-[1_1_120px] flex-col gap-1.5">
            <span>
              Time{" "}
              <span className="text-mut">
                (
                {settings.time_zone === "Local"
                  ? "server local"
                  : settings.time_zone}
                )
              </span>
            </span>
            <select
              value={hour}
              onChange={(e) => setHour(Number(e.target.value))}
              className="py-[7px] text-sm"
            >
              {Array.from({ length: 24 }, (_, h) => (
                <option key={h} value={h}>
                  {hourLabel(h)}
                </option>
              ))}
            </select>
          </label>
        </div>
        {/* Only a changed form has anything to save. */}
        {(dirty || configure.error) && (
          <div className="flex flex-wrap items-center gap-2.5">
            <Act
              primary
              type="submit"
              cli={`dock backup configure${changes.keep_daily !== undefined ? ` --keep-daily ${changes.keep_daily}` : ""}${changes.keep_weekly !== undefined ? ` --keep-weekly ${changes.keep_weekly}` : ""}${changes.keep_monthly !== undefined ? ` --keep-monthly ${changes.keep_monthly}` : ""}${changes.hour !== undefined ? ` --hour ${changes.hour}` : ""}`}
              disabled={!dirty || configure.isPending}
            >
              Save
            </Act>
            <Button
              onClick={() => {
                configure.reset();
                setKeep({
                  daily: settings.keep_daily,
                  weekly: settings.keep_weekly,
                  monthly: settings.keep_monthly,
                });
                setHour(settings.hour);
              }}
            >
              Discard
            </Button>
            <ErrorText error={configure.error} />
          </div>
        )}
        {!dirty && configure.isSuccess && (
          <span className="text-xs text-ok">Saved</span>
        )}
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
