import { type ReactNode, useState } from "react";
import { ago, clock, day, size, until } from "../../ui/format";
import {
  Act,
  Button,
  CopyButton,
  ErrorText,
  Page,
  SectionHead,
  StateText,
  TitleRow,
} from "../../ui/kit";
import type { State } from "../../ui/status";
import { hourLabel } from "../backups/backups";
import {
  type BackupConfig,
  type BackupSettings,
  type ControlPlaneRun,
  useBackupSettings,
  useConfigureBackups,
  useRevealedPassword,
  useRunControlPlaneBackup,
} from "./data";

// The install's backups, in the platform console (admins): the control
// plane's own daily backup with Back up now (`dock backup run
// --control-plane`), and where every backup goes, retention and the
// default hour (`dock backup status`, `dock backup configure`).

const runStates: Record<ControlPlaneRun["status"], State> = {
  running: { tone: "prog", glyph: "◐", label: "running" },
  succeeded: { tone: "ok", glyph: "●", label: "succeeded" },
  failed: { tone: "fail", glyph: "✕", label: "failed" },
};

/** "Oct 24 03:00, in 5h" */
const nextText = (iso: string) => `${day(iso)} ${clock(iso)}, in ${until(iso)}`;

export function BackupsPage() {
  const settings = useBackupSettings();
  const run = useRunControlPlaneBackup();
  // Here, not in the form: the form remounts with the saved settings.
  const save = useConfigureBackups();
  // The password from the configure call that generated it, shown once.
  const [generated, setGenerated] = useState("");
  const running = settings.control_plane.latest?.status === "running";

  return (
    <Page width="max-w-[960px]">
      <TitleRow
        title="Backups"
        context={
          settings.off_box ? (
            <>
              Off-box to{" "}
              <span className="font-mono">{settings.repository}</span>
            </>
          ) : (
            <span className="text-warn">
              Local on each server, not off-box: losing a server loses its
              backups. Set an S3 repository below.
            </span>
          )
        }
        actions={
          <>
            <Act
              primary
              cli="dock backup run --control-plane"
              disabled={running || run.isPending}
              title={running ? "A control plane backup is running" : undefined}
              onClick={() => run.mutate()}
            >
              Back up now
            </Act>
            <ErrorText error={run.error} />
          </>
        }
      />
      {generated && (
        <Generated password={generated} onDone={() => setGenerated("")} />
      )}
      <ControlPlane settings={settings} />
      <Repository settings={settings} />
      <Configure
        // Fresh fields whenever the saved settings change.
        key={settings.updated_at ?? ""}
        settings={settings}
        save={save}
        onGenerated={setGenerated}
      />
    </Page>
  );
}

/** The control plane's latest backup and the next one. */
function ControlPlane({ settings }: { settings: BackupSettings }) {
  const { latest, next_at } = settings.control_plane;
  return (
    <section aria-labelledby="control-plane">
      <SectionHead id="control-plane" aside="database, keys and config, daily">
        Control plane
      </SectionHead>
      <dl className="props grid-cols-[140px_minmax(0,1fr)]">
        <Item label="Latest">
          {latest ? (
            <>
              <StateText state={runStates[latest.status]} />{" "}
              <span className="text-mut">
                {latest.trigger === "manual"
                  ? `by ${latest.created_by}`
                  : "scheduled"}
                , started {ago(latest.started_at)}
              </span>
            </>
          ) : (
            <span className="text-mut">no backup yet</span>
          )}
        </Item>
        {latest?.status === "succeeded" && (
          <Item label="Snapshot">
            <span className="font-mono">{latest.snapshot?.slice(0, 8)}</span>
            <span className="text-mut">
              {" "}
              · {size(latest.size_bytes)} ·{" "}
              {latest.off_box ? latest.repository : "local, not off-box"}
            </span>
          </Item>
        )}
        {latest?.status === "failed" && (
          <Item label="Error">
            <span className="text-fail">{latest.error}</span>
            <span className="block text-mut">
              Check the repository and its credentials below, then back up
              again.
            </span>
          </Item>
        )}
        <Item label="Next">
          {next_at ? (
            nextText(next_at)
          ) : (
            <span className="text-mut">not scheduled</span>
          )}
        </Item>
      </dl>
    </section>
  );
}

/** What the repository has now, and its password on demand. */
function Repository({ settings }: { settings: BackupSettings }) {
  const [shown, setShown] = useState(false);
  const password = useRevealedPassword(shown);
  return (
    <section aria-labelledby="repository">
      <SectionHead id="repository" aside="dock backup status">
        Repository
      </SectionHead>
      <dl className="props grid-cols-[140px_minmax(0,1fr)]">
        <Item label="Where">
          {settings.off_box ? (
            <span className="font-mono">{settings.repository}</span>
          ) : (
            <span className="text-warn">local repository on each server</span>
          )}
        </Item>
        <Item label="Credentials">
          {settings.has_credentials ? (
            "set"
          ) : (
            <span className="text-mut">none</span>
          )}
        </Item>
        <Item label="Retention">
          {settings.keep_daily} daily, {settings.keep_weekly} weekly,{" "}
          {settings.keep_monthly} monthly
        </Item>
        <Item label="Schedule">
          daily at {hourLabel(settings.hour)}{" "}
          <span className="text-mut">{settings.time_zone}</span>
        </Item>
        <Item label="Password">
          {shown && password.data ? (
            <span className="flex flex-wrap items-center gap-2.5">
              <code className="font-mono">{password.data}</code>
              <CopyButton text={password.data} />
              <Button small onClick={() => setShown(false)}>
                Hide
              </Button>
            </span>
          ) : (
            <span className="flex flex-wrap items-center gap-2.5">
              <span className="text-mut">
                without it the backups can't be read
              </span>
              <Act
                small
                cli="dock backup status --reveal"
                disabled={password.isFetching}
                onClick={() => setShown(true)}
              >
                Reveal
              </Act>
            </span>
          )}
          <ErrorText error={password.error} />
        </Item>
        <Item label="restic">
          <span className="font-mono text-xs">{settings.restic_image}</span>
        </Item>
        {settings.updated_at && (
          <Item label="Changed">
            {ago(settings.updated_at)}
            {settings.updated_by && ` by ${settings.updated_by}`}
          </Item>
        )}
      </dl>
    </section>
  );
}

/** The repository password the first configure generated. */
function Generated({
  password,
  onDone,
}: {
  password: string;
  onDone: () => void;
}) {
  return (
    <section
      aria-labelledby="generated"
      className="flex flex-col gap-2.5 rounded-panel border border-warn-edge p-4"
    >
      <h2 id="generated" className="font-semibold">
        Store the repository password now
      </h2>
      <p className="text-mut">
        Put it in a password manager. Without it the backups can't be read after
        losing the server. Admins can show it again with Reveal.
      </p>
      <div className="flex flex-wrap items-center gap-2.5 rounded-ctl border border-line bg-side px-3 py-2.5">
        <code className="min-w-0 flex-1 overflow-x-auto font-mono whitespace-nowrap">
          {password}
        </code>
        <CopyButton text={password} primary />
      </div>
      <div>
        <Button small onClick={onDone}>
          I've stored it
        </Button>
      </div>
    </section>
  );
}

/** The `dock backup configure` flags for a request. */
function configureCli(req: BackupConfig) {
  const flags: Array<string | false | undefined> = [
    req.repository !== undefined &&
      `--repository ${req.repository ? req.repository : '""'}`,
    req.access_key_id !== undefined && `--access-key-id ${req.access_key_id}`,
    req.secret_access_key !== undefined && "--secret-access-key-stdin",
    req.region !== undefined && `--region ${req.region}`,
    req.keep_daily !== undefined && `--keep-daily ${req.keep_daily}`,
    req.keep_weekly !== undefined && `--keep-weekly ${req.keep_weekly}`,
    req.keep_monthly !== undefined && `--keep-monthly ${req.keep_monthly}`,
    req.hour !== undefined && `--hour ${req.hour}`,
  ];
  return ["dock backup configure", ...flags.filter(Boolean)].join(" ");
}

/**
 * Where backups go, retention and the hour. Like the CLI, only what
 * changed is sent. The S3 key ID, secret and region are never shown back:
 * empty fields keep what is stored.
 */
function Configure({
  settings,
  save,
  onGenerated,
}: {
  settings: BackupSettings;
  save: ReturnType<typeof useConfigureBackups>;
  onGenerated: (password: string) => void;
}) {
  const [repository, setRepository] = useState(settings.repository);
  const [accessKey, setAccessKey] = useState("");
  const [secret, setSecret] = useState("");
  const [region, setRegion] = useState("");
  const [keepDaily, setKeepDaily] = useState(settings.keep_daily);
  const [keepWeekly, setKeepWeekly] = useState(settings.keep_weekly);
  const [keepMonthly, setKeepMonthly] = useState(settings.keep_monthly);
  const [hour, setHour] = useState(settings.hour);

  const changed = <T,>(value: T, saved: T) =>
    value === saved ? undefined : value;
  const req: BackupConfig = {
    repository: changed(repository.trim(), settings.repository),
    access_key_id: accessKey.trim() || undefined,
    secret_access_key: secret.trim() || undefined,
    region: region.trim() || undefined,
    keep_daily: changed(keepDaily, settings.keep_daily),
    keep_weekly: changed(keepWeekly, settings.keep_weekly),
    keep_monthly: changed(keepMonthly, settings.keep_monthly),
    hour: changed(hour, settings.hour),
  };
  const dirty = Object.values(req).some((v) => v !== undefined);
  const keep = settings.has_credentials ? "unchanged" : "";
  const toLocal = req.repository === "" && settings.off_box;

  return (
    <section aria-labelledby="configure">
      <SectionHead id="configure">Configure</SectionHead>
      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (!dirty) return;
          save.mutate(req, {
            onSuccess: (s) => s.password && onGenerated(s.password),
          });
        }}
      >
        <label className="flex flex-col gap-1.5">
          <span>
            Repository{" "}
            <span className="text-xs text-faint">
              any S3-compatible store; empty for local repositories
            </span>
          </span>
          <input
            value={repository}
            onChange={(e) => setRepository(e.target.value)}
            placeholder="s3:https://s3.eu-central-1.amazonaws.com/bucket/dock"
            spellCheck={false}
            className="field py-[7px] font-mono"
          />
          {toLocal && (
            <span className="text-xs text-warn">
              Backups go back to local repositories, not off-box.
            </span>
          )}
        </label>
        <div className="flex flex-wrap gap-3">
          <Text
            label="Access key ID"
            value={accessKey}
            onChange={setAccessKey}
            placeholder={keep}
            mono
          />
          <Text
            label="Secret access key"
            value={secret}
            onChange={setSecret}
            placeholder={keep}
            secret
          />
          <Text
            label="Region"
            value={region}
            onChange={setRegion}
            placeholder={keep || "if the store needs one"}
          />
        </div>
        <div className="flex flex-wrap gap-3">
          <Num
            label="Keep daily"
            value={keepDaily}
            onChange={setKeepDaily}
            min={1}
            max={365}
          />
          <Num
            label="Keep weekly"
            value={keepWeekly}
            onChange={setKeepWeekly}
            min={0}
            max={520}
          />
          <Num
            label="Keep monthly"
            value={keepMonthly}
            onChange={setKeepMonthly}
            min={0}
            max={240}
          />
          <label className="flex flex-[1_1_140px] flex-col gap-1.5">
            <span>
              Daily at{" "}
              <span className="text-xs text-faint">{settings.time_zone}</span>
            </span>
            <select
              value={hour}
              onChange={(e) => setHour(Number(e.target.value))}
              className="field py-[7px] font-mono"
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
          <button
            type="submit"
            className="btn pri"
            disabled={!dirty || save.isPending}
          >
            Save
          </button>
          <span className="font-mono text-2xs text-mut">
            {dirty ? configureCli(req) : "dock backup configure"}
          </span>
          {save.isSuccess && !dirty && <span className="text-ok">✓ Saved</span>}
          <ErrorText error={save.error} />
        </div>
      </form>
    </section>
  );
}

function Text({
  label,
  value,
  onChange,
  placeholder,
  secret,
  mono,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
  secret?: boolean;
  mono?: boolean;
}) {
  return (
    <label className="flex flex-[1_1_200px] flex-col gap-1.5">
      <span>{label}</span>
      <input
        type={secret ? "password" : "text"}
        autoComplete={secret ? "new-password" : "off"}
        spellCheck={false}
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        className={`field py-[7px] ${mono ? "font-mono" : ""}`}
      />
    </label>
  );
}

function Num({
  label,
  value,
  onChange,
  min,
  max,
}: {
  label: string;
  value: number;
  onChange: (value: number) => void;
  min: number;
  max: number;
}) {
  return (
    <label className="flex flex-[1_1_100px] flex-col gap-1.5">
      <span>{label}</span>
      <input
        type="number"
        min={min}
        max={max}
        value={value}
        onChange={(e) => onChange(e.target.valueAsNumber || 0)}
        className="field py-[7px] font-mono"
      />
    </label>
  );
}

function Item({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt>{label}</dt>
      <dd>{children}</dd>
    </>
  );
}
