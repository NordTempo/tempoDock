import { useState } from "react";
import { ago, since } from "../../ui/format";
import {
  Act,
  Button,
  Confirm,
  ErrorText,
  Glyph,
  Prop,
  SectionHead,
} from "../../ui/kit";
import { invalid, useValidation } from "../create/form";
import {
  type Heartbeat as HeartbeatState,
  isHeartbeatAlert,
  useHeartbeat,
  useOpenAlerts,
  useRemoveHeartbeat,
  useSetHeartbeat,
  useTestHeartbeat,
} from "./data";

// The platform's outside heartbeat (ADR 0097), on the console's Alerts
// page under its channels: a URL Dock requests every few minutes while
// it runs, so a monitor outside this server notices when the pings
// stop. One status line; the URL is a secret and only ever typed here.

const what =
  "Dock requests this URL every few minutes while it runs, so a monitor outside this server notices when the pings stop.";

/** The heartbeat's section: its status line, or the form that sets it. */
export function Heartbeat() {
  const heartbeat = useHeartbeat();
  const [editing, setEditing] = useState(false);
  const test = useTestHeartbeat();
  const remove = useRemoveHeartbeat();
  const [confirming, setConfirming] = useState(false);
  const result = test.data;
  return (
    <section aria-labelledby="heartbeat" className="flex flex-col gap-2">
      <SectionHead
        id="heartbeat"
        right={
          !editing &&
          (heartbeat ? (
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
                cli="dock alerts heartbeat test"
                disabled={test.isPending}
                onClick={() => test.mutate()}
              >
                Test
              </Act>
              <Button small onClick={() => setEditing(true)}>
                Change
              </Button>
              <Button danger small onClick={() => setConfirming(true)}>
                Remove
              </Button>
            </span>
          ) : (
            <Button small onClick={() => setEditing(true)}>
              Set heartbeat
            </Button>
          ))
        }
      >
        Outside heartbeat
      </SectionHead>
      {editing ? (
        <SetHeartbeat
          minutes={heartbeat?.minutes}
          onClose={() => {
            // The last test was of the heartbeat this one replaces.
            test.reset();
            setEditing(false);
          }}
        />
      ) : heartbeat ? (
        <Status heartbeat={heartbeat} />
      ) : (
        <p className="flex flex-wrap gap-x-3 gap-y-1">
          <span className="text-mut">Not set.</span>
          <span>{what}</span>
        </p>
      )}
      {confirming && heartbeat && (
        <Confirm
          danger
          action="Remove the heartbeat"
          cli="dock alerts heartbeat remove"
          pending={remove.isPending}
          error={remove.error}
          onConfirm={() =>
            remove.mutate(undefined, {
              onSuccess: () => {
                test.reset();
                setConfirming(false);
              },
            })
          }
          onCancel={() => {
            remove.reset();
            setConfirming(false);
          }}
        >
          Dock stops requesting <b>{heartbeat.target}</b>. The monitor there
          will raise its alarm unless it is removed or paused too.
        </Confirm>
      )}
    </section>
  );
}

/**
 * How the pings are going, on one line; while they fail, the last error,
 * when one last went through and the alert they opened.
 */
function Status({ heartbeat: hb }: { heartbeat: HeartbeatState }) {
  const alert = useOpenAlerts("platform").find(isHeartbeatAlert);
  const failing = hb.failures > 0;
  const sent = hb.last_sent_at ? ago(hb.last_sent_at) : undefined;
  return (
    <>
      <p className="flex flex-wrap items-baseline gap-x-4 gap-y-1">
        {failing ? (
          <>
            <span className="text-fail">
              <Glyph char="✕" /> failing
            </span>
            <span>
              {hb.failures}{" "}
              {hb.failures === 1 ? "failure" : "failures in a row"}
            </span>
          </>
        ) : sent ? (
          <>
            <span className="text-ok">
              <Glyph char="●" /> working
            </span>
            <span>last sent {sent}</span>
          </>
        ) : (
          <span className="text-mut">
            <Glyph char="○" /> no ping sent yet
          </span>
        )}
        <span className="font-mono">{hb.target}</span>
        <span>every {hb.minutes} min</span>
      </p>
      {failing && (
        <dl className="props max-w-[760px] [grid-template-columns:120px_minmax(0,1fr)]">
          <Prop label="Last error">
            <span className="text-fail">{hb.last_error}</span>
            {hb.last_error_at && (
              <span className="text-mut"> {ago(hb.last_error_at)}</span>
            )}
          </Prop>
          <Prop label="Last sent">{sent ?? "not since Dock started"}</Prop>
          {alert && (
            <Prop label="Alert">
              <a href="#open">{alert.title}</a>{" "}
              <span className="text-mut">open {since(alert.opened_at)}</span>
            </Prop>
          )}
        </dl>
      )}
      <p className="text-xs text-mut">
        {what} <span className="font-mono text-2xs">dock alerts heartbeat</span>
      </p>
    </>
  );
}

/**
 * The form that sets the heartbeat, or replaces it: the URL is never
 * returned, so a change types it again.
 */
function SetHeartbeat({
  minutes,
  onClose,
}: {
  /** The interval now, when one is set. */
  minutes: number | undefined;
  onClose: () => void;
}) {
  const [url, setUrl] = useState("");
  const [every, setEvery] = useState(String(minutes ?? 5));
  const set = useSetHeartbeat(() => url.trim());
  const v = useValidation();
  const n = Number(every);
  const urlError =
    v.required(!url.trim(), "Paste the URL to request.") ??
    (url.trim() && !/^https?:\/\/\S+$/.test(url.trim())
      ? "A URL starting with http:// or https://."
      : undefined);
  const everyError =
    Number.isInteger(n) && n >= 1 && n <= 60
      ? undefined
      : "A whole number from 1 to 60.";
  return (
    <form
      ref={v.form}
      aria-label="Set the outside heartbeat"
      className="flex max-w-[760px] flex-col gap-3.5"
      onKeyDown={(e) => e.key === "Escape" && onClose()}
      onSubmit={(e) => {
        e.preventDefault();
        if (!v.check(!!url.trim() && !urlError && !everyError)) return;
        set.mutate(n, { onSuccess: onClose });
      }}
    >
      <p>{what}</p>
      <div className="flex flex-wrap gap-3">
        <label className="flex flex-[1_1_420px] flex-col gap-1.5">
          <span>
            URL{" "}
            <span className="text-mut">
              stored encrypted, only the host is shown after saving
            </span>
          </span>
          <input
            // biome-ignore lint/a11y/noAutofocus: the form opens on request
            autoFocus
            autoComplete="off"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://"
            aria-invalid={!!urlError}
            className={`field font-mono ${invalid(urlError)}`}
          />
          {urlError && (
            <span role="alert" className="text-xs text-fail">
              {urlError}
            </span>
          )}
        </label>
        <label className="flex flex-[0_1_180px] flex-col gap-1.5">
          <span>
            Every <span className="text-mut">minutes, 1 to 60</span>
          </span>
          <input
            type="number"
            min={1}
            max={60}
            step={1}
            value={every}
            onChange={(e) => setEvery(e.target.value)}
            aria-invalid={!!everyError}
            className={`field font-mono ${invalid(everyError)}`}
          />
          {everyError && (
            <span role="alert" className="text-xs text-fail">
              {everyError}
            </span>
          )}
        </label>
      </div>
      <div className="flex flex-wrap items-center gap-2.5">
        <Act
          primary
          type="submit"
          cli={`dock alerts heartbeat set -${n === 5 ? "" : ` --every ${every}`}`}
          disabled={set.isPending}
        >
          Save
        </Act>
        <Button onClick={onClose}>Cancel</Button>
        <ErrorText error={set.error} />
      </div>
    </form>
  );
}
