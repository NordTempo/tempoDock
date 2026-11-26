import { useState } from "react";
import { size } from "../../ui/format";
import { Act, Button, ErrorText, Page, PageBar, Panel } from "../../ui/kit";
import {
  type Feature,
  type FeatureKey,
  useFeatures,
  useSwitchFeature,
} from "./features";

/**
 * What each feature is called, what it does, what turning it off keeps or
 * removes, and its CLI: the console's words for ADR 0101.
 */
const about: Record<
  FeatureKey,
  {
    label: string;
    detail: string;
    /** The data off may remove, or null when off always removes it. */
    data: string | null;
    gone?: string;
    cli: (on: boolean, remove: boolean) => string;
  }
> = {
  scanning: {
    label: "Vulnerability scanning",
    detail: "jobs only while nothing deploys; the database updates daily",
    data: "the vulnerability database",
    cli: (on, remove) =>
      on
        ? "dock scanning on"
        : `dock scanning off${remove ? " --remove-database" : ""}`,
  },
  analytics: {
    label: "Analytics",
    detail: "the store for sites' page views, events, errors and replays",
    data: "every site's events, replays and errors",
    cli: (on, remove) =>
      on
        ? "dock analytics on"
        : `dock analytics off${remove ? " --remove-data --yes" : ""}`,
  },
  tracing: {
    label: "Tracing",
    detail: "spans and app metrics from services that send them",
    data: null,
    gone: "its spans go with the trace store",
    cli: (on) => (on ? "dock tracing on" : "dock tracing off"),
  },
};

/**
 * The console's Features page (ADR 0101): every optional feature with the
 * servers it runs on, what it takes now and a switch. On switches at once;
 * off asks first, with the choice to keep or remove its data.
 */
export function FeaturesPage() {
  const features = useFeatures();
  const set = useSwitchFeature();
  const [asking, setAsking] = useState<FeatureKey | null>(null);
  const [remove, setRemove] = useState(false);
  const items = features.data?.items ?? [];
  const ask = (key: FeatureKey) => {
    set.reset();
    setRemove(false);
    setAsking(key);
  };
  return (
    <>
      <PageBar title="Features" />
      <Page>
        <ErrorText error={features.error} />
        <Panel>
          <table>
            <thead>
              <tr>
                <th>Feature</th>
                <th>Runs on</th>
                <th className="text-right">Memory</th>
                <th className="text-right">Disk</th>
                <th />
                <th className="w-full" />
              </tr>
            </thead>
            <tbody>
              {items.map((f) => (
                <FeatureRows
                  key={f.key}
                  feature={f}
                  asking={asking === f.key}
                  remove={remove}
                  pending={set.isPending}
                  error={asking === f.key || !asking ? set.error : null}
                  onRemove={setRemove}
                  onSwitch={() =>
                    f.enabled
                      ? ask(f.key)
                      : set.mutate({
                          key: f.key,
                          enabled: true,
                          removeData: false,
                        })
                  }
                  onOff={() =>
                    set.mutate(
                      { key: f.key, enabled: false, removeData: remove },
                      { onSuccess: () => setAsking(null) },
                    )
                  }
                  onCancel={() => setAsking(null)}
                />
              ))}
            </tbody>
          </table>
        </Panel>
      </Page>
    </>
  );
}

/** One feature's row, and under it the question off asks. */
function FeatureRows({
  feature: f,
  asking,
  remove,
  pending,
  error,
  onRemove,
  onSwitch,
  onOff,
  onCancel,
}: {
  feature: Feature;
  asking: boolean;
  remove: boolean;
  pending: boolean;
  error: Error | null;
  onRemove: (remove: boolean) => void;
  onSwitch: () => void;
  onOff: () => void;
  onCancel: () => void;
}) {
  const a = about[f.key];
  const disk = f.disk_bytes ?? null;
  return (
    <>
      <tr>
        <td className="whitespace-nowrap">{a.label}</td>
        <td className="font-mono whitespace-nowrap text-sec">
          {f.servers?.join(", ") || "·"}
        </td>
        <td className="text-right font-mono whitespace-nowrap">
          {f.enabled && f.memory_bytes != null
            ? size(f.memory_bytes)
            : `up to ${size(f.memory_limit_bytes)}`}
        </td>
        <td className="text-right font-mono whitespace-nowrap">
          {disk == null ? <span className="text-faint">·</span> : size(disk)}
        </td>
        <td>
          <Switch
            label={a.label}
            on={f.enabled}
            disabled={pending || asking}
            onClick={onSwitch}
          />
        </td>
        <td className="text-mut">
          {f.enabled ? a.detail : "off"}
          {!asking && error && (
            <>
              {" "}
              <ErrorText error={error} />
            </>
          )}
        </td>
      </tr>
      {asking && (
        <tr className="bg-panel">
          <td colSpan={6} className="py-3">
            <div className="mb-1.5 font-medium">
              Turn {a.label.toLowerCase()} off?
            </div>
            {a.data ? (
              <fieldset className="flex flex-col gap-1 text-sec">
                <legend className="sr-only">What happens to its data</legend>
                <label className="flex items-center gap-2">
                  <input
                    type="radio"
                    name={`${f.key}-data`}
                    checked={!remove}
                    onChange={() => onRemove(false)}
                  />
                  Keep {a.data}
                  {disk != null && ` (${size(disk)})`}: on again, it is there
                </label>
                <label className="flex items-center gap-2">
                  <input
                    type="radio"
                    name={`${f.key}-data`}
                    checked={remove}
                    onChange={() => onRemove(true)}
                  />
                  Remove {a.data}
                  {disk != null && `: frees ${size(disk)}`}
                  {f.key === "scanning"
                    ? ", downloaded again when turned on"
                    : ", for good"}
                </label>
              </fieldset>
            ) : (
              <p className="text-sec">{a.gone}.</p>
            )}
            <div className="mt-2.5 flex flex-wrap items-center gap-2">
              <Act
                small
                danger
                cli={a.cli(false, remove)}
                disabled={pending}
                onClick={onOff}
              >
                {pending
                  ? "Turning off…"
                  : remove && disk != null
                    ? `Turn off and remove ${size(disk)}`
                    : "Turn off"}
              </Act>
              <Button small onClick={onCancel}>
                Cancel
              </Button>
              <ErrorText error={error} />
            </div>
          </td>
        </tr>
      )}
    </>
  );
}

/** An on/off switch: a button that says its state to screen readers. */
function Switch({
  label,
  on,
  disabled,
  onClick,
}: {
  label: string;
  on: boolean;
  disabled: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={on}
      aria-label={label}
      disabled={disabled}
      onClick={onClick}
      className="inline-flex items-center align-middle disabled:opacity-50"
    >
      {/* The track inside the button, so the button's own height (the
          kit's tap target) doesn't move the knob. */}
      <span
        aria-hidden
        className={`relative h-[17px] w-[30px] rounded-full ${on ? "bg-ok-edge" : "bg-edge"}`}
      >
        <span
          className={`absolute top-[2px] size-[13px] rounded-full ${on ? "left-[15px] bg-ok" : "left-[2px] bg-off"}`}
        />
      </span>
    </button>
  );
}
