import { clock, plural, since, size } from "../../ui/format";
import { Glyph, Prop, SectionHead } from "../../ui/kit";
import { type State, toneText } from "../../ui/status";
import type { Registry } from "./data";

type Bucket = NonNullable<Registry["bucket"]>;

// The Registry page's Storage section (ADR 0084): the bucket's health
// from the calls the storage gateway makes to it anyway, and the spool on
// the control plane's disk where pushes land before they upload. One
// line while all is well; a table of what is wrong otherwise.

const bucketStates: Record<Bucket["state"], State> = {
  healthy: { tone: "ok", glyph: "●", label: "healthy" },
  degraded: { tone: "warn", glyph: "▲", label: "degraded" },
  failing: { tone: "fail", glyph: "✕", label: "failing" },
};

export function BucketSection({ bucket: b }: { bucket: Bucket }) {
  const stuck = b.stuck ?? [];
  const quiet =
    b.state === "healthy" && !b.refusing && b.waiting === 0 && !stuck.length;
  if (quiet) {
    return (
      <section aria-labelledby="bucket">
        {/* The state beside the heading, not at the far edge, so the one
            line reads as one. */}
        <SectionHead
          id="bucket"
          aside={
            <>
              <Tone state={bucketStates.healthy} /> · nothing waiting to upload
            </>
          }
        >
          Storage
        </SectionHead>
      </section>
    );
  }
  const spoolPct = b.spool_max_bytes
    ? Math.round((b.spool_bytes / b.spool_max_bytes) * 100)
    : 0;
  const diskUsedPct = b.disk_bytes
    ? Math.round(((b.disk_bytes - b.disk_free_bytes) / b.disk_bytes) * 100)
    : 0;
  return (
    <section aria-labelledby="bucket">
      <SectionHead id="bucket">Storage</SectionHead>
      <dl className="props grid-cols-[140px_minmax(0,1fr)] [&>dd]:flex [&>dd]:flex-wrap [&>dd]:items-baseline [&>dd]:gap-x-2.5 [&>dd]:gap-y-1">
        <Prop label="Bucket">
          <Tone
            state={{
              ...bucketStates[b.state],
              label: `${bucketStates[b.state].label} since ${clock(b.since)}`,
            }}
          />
          <span className="text-xs text-mut">
            {b.bad} of the last {b.calls} calls failed or took over 10 s; the
            slowest {seconds(b.slowest_ms)}
          </span>
          {b.last_error && (
            <span className="w-full font-mono text-xs break-all text-mut">
              {b.last_error}
            </span>
          )}
        </Prop>
        <Prop label="Waiting to upload">
          {b.waiting === 0 ? (
            <span className="text-mut">nothing</span>
          ) : (
            <>
              <span>
                {plural(b.waiting, "object")}, {size(b.waiting_bytes)}
              </span>
              {b.oldest_waiting_at && (
                <span className="text-xs text-mut">
                  the oldest for {since(b.oldest_waiting_at)}
                </span>
              )}
            </>
          )}
        </Prop>
        <Prop label="Spool">
          <Bar
            label="Spool used"
            percent={spoolPct}
            tone={b.refusing === "spool" ? "fail" : b.waiting ? "warn" : "mut"}
          />
          <span>
            {size(b.spool_bytes)} of {size(b.spool_max_bytes)}
          </span>
          {b.refusing === "spool" ? (
            <span className="text-xs text-fail">full</span>
          ) : (
            <span className="text-xs text-mut">{spoolPct}%</span>
          )}
        </Prop>
        {b.refusing && (
          <Prop label="Disk">
            <Bar
              label="Disk used"
              percent={diskUsedPct}
              tone={b.refusing === "disk" ? "fail" : "mut"}
            />
            <span>
              {size(b.disk_free_bytes)} free of {size(b.disk_bytes)}
            </span>
            <span
              className={`text-xs ${b.refusing === "disk" ? "text-fail" : "text-mut"}`}
            >
              {b.refusing === "disk"
                ? `under the ${b.disk_min_free_percent}% Dock keeps`
                : `Dock keeps ${b.disk_min_free_percent}% free`}
            </span>
          </Prop>
        )}
        <Prop label="Pushes">
          {b.refusing ? (
            <>
              <Tone
                state={{
                  tone: "fail",
                  glyph: "✕",
                  label:
                    b.refusing === "spool"
                      ? "refused: the spool is full"
                      : "refused: the disk is nearly full",
                }}
              />
              <span className="text-xs text-mut">
                {b.refusing === "spool"
                  ? "builds fail at the push until uploads free space"
                  : "builds fail at the push until the disk has room"}
              </span>
            </>
          ) : (
            <>
              <span>accepted</span>
              <span className="text-xs text-mut">
                they land in the spool and upload in the background
              </span>
            </>
          )}
        </Prop>
        {stuck.length > 0 && (
          <Prop label="Stuck blobs">
            {stuck.map((s, i) => (
              <span key={s.digest} className="flex items-baseline gap-2.5">
                <span className="font-mono" title={s.digest}>
                  {s.digest.slice(0, 15)}…
                </span>
                <span className="text-xs text-mut">
                  since {clock(s.since)}
                  {i === stuck.length - 1 &&
                    ` · Docker uploads ${stuck.length > 1 ? "them" : "it"} again on the next push`}
                </span>
              </span>
            ))}
          </Prop>
        )}
      </dl>
      {b.waiting > 0 && (
        <p className="mt-2 text-mut">
          What waits in the spool is only on this server's disk until it
          uploads.
        </p>
      )}
    </section>
  );
}

/** A state as colored text with its glyph: "▲ degraded since 14:02". */
function Tone({ state }: { state: State }) {
  return (
    <span className={toneText[state.tone]}>
      {state.glyph && <Glyph char={state.glyph} />} {state.label}
    </span>
  );
}

/** A thin fixed-width bar, filled to percent; a progressbar named `label`. */
function Bar({
  label,
  percent,
  tone,
}: {
  label: string;
  percent: number;
  tone: "mut" | "warn" | "fail";
}) {
  const fill = { mut: "bg-sec", warn: "bg-warn", fail: "bg-fail" }[tone];
  return (
    <span
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={percent}
      className="inline-block h-1 w-30 self-center overflow-hidden rounded-full bg-chip"
    >
      <span
        className={`block h-full ${fill}`}
        style={{
          width: `${Math.min(100, Math.max(percent, percent > 0 ? 1 : 0))}%`,
        }}
      />
    </span>
  );
}

/** "31 s", "450 ms" */
function seconds(ms: number) {
  return ms < 1000 ? `${ms} ms` : `${Math.round(ms / 1000)} s`;
}
