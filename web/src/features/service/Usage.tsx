import type { Service } from "../../data/types";
import { cpus, pct, size } from "../../ui/format";

// A service's resource use against its effective limit, as in the overview
// and on server pages: "231M / 512M", "12% of 1 CPU". The limit is the
// service's own or the server's default (the tooltip says which).
// Usage is the latest sample, at most a minute old (live on the Resources
// tab). Without a running container, the limit alone: limitText.

/** What usage cells need: a service, or a service on a server page. */
type Limited = Pick<
  Service,
  | "usage"
  | "effective_memory_mb"
  | "memory_from_default"
  | "effective_cpus"
  | "cpus_from_default"
>;

const MiB = 1024 * 1024;

/** Near the limit: worth a look before it runs out. */
const HIGH = 0.9;

/** Says in a tooltip that a limit is the server's default, to keep cells short. */
const limitTitle = (fromDefault: boolean) =>
  fromDefault
    ? "The server's default limit; the service sets none"
    : "The service's own limit";

/**
 * A limit in the one wording every screen uses: "512M limit", "1 CPU
 * default" (the service sets none, the server's default applies), or
 * "no limit".
 */
export function limitText(s: Limited, what: "cpu" | "memory") {
  const [value, fromDefault] =
    what === "cpu"
      ? [s.effective_cpus ? cpus(s.effective_cpus) : "", s.cpus_from_default]
      : [
          s.effective_memory_mb ? size(s.effective_memory_mb * MiB) : "",
          s.memory_from_default,
        ];
  if (!value) return "no limit";
  return fromDefault ? `${value} (default)` : value;
}

/**
 * A limit in a table cell, where "(default)" on every row is noise (a
 * blind trial, 2026-11): the value alone, dimmer when it is the server's
 * default, with the tooltip saying which.
 */
function LimitCell({ s, what }: { s: Limited; what: "cpu" | "memory" }) {
  const fromDefault =
    what === "cpu" ? s.cpus_from_default : s.memory_from_default;
  const text = limitText(s, what).replace(" (default)", "");
  return (
    <span
      className={fromDefault ? "text-faint" : "text-sec"}
      title={text === "no limit" ? undefined : limitTitle(fromDefault)}
    >
      {text}
    </span>
  );
}

/** Share of the memory limit in use, 0 to 1, or null without a sample. */
export const memoryShare = (s: Limited) =>
  s.usage && s.effective_memory_mb
    ? s.usage.memory_bytes / (s.effective_memory_mb * MiB)
    : null;

/** "231M / 512M", or the limit alone when nothing is running. */
export function MemoryUsage({ service: s }: { service: Limited }) {
  const limit = s.effective_memory_mb ? size(s.effective_memory_mb * MiB) : "";
  const share = memoryShare(s);
  if (!s.usage) return <LimitCell s={s} what="memory" />;
  return (
    <span>
      <span className={share !== null && share >= HIGH ? "text-warn" : ""}>
        {size(s.usage.memory_bytes)}
      </span>
      {limit && (
        <span className="text-mut" title={limitTitle(s.memory_from_default)}>
          {" "}
          / {limit}
        </span>
      )}
    </span>
  );
}

/** "12% of 1 CPU": usage as a share of the CPU limit. */
export function CpuUsage({ service: s }: { service: Limited }) {
  const limit = s.effective_cpus;
  if (!s.usage) return <LimitCell s={s} what="cpu" />;
  // cpu_percent is relative to one core; the limit may be more or less.
  const share = limit ? s.usage.cpu_percent / limit : s.usage.cpu_percent;
  return (
    <span>
      <span className={limit && share >= HIGH * 100 ? "text-warn" : ""}>
        {pct(share)}
      </span>
      <span
        className="text-mut"
        title={limit ? limitTitle(s.cpus_from_default) : undefined}
      >
        {limit ? ` of ${cpus(limit)}` : " of a core"}
      </span>
    </span>
  );
}
