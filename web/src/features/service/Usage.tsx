import type { Service } from "../../data/types";
import { cpus, pct, size } from "../../ui/format";

// A service's resource use against its effective limit, as in the overview
// and on server pages: "231M / 512M", "12% of 1 CPU". The limit is the
// service's own or the server default ("default" says which). Usage is the
// latest sample, at most a minute old (live on the Metrics tab). Without
// a running container, the limit alone: "512M limit", "1 CPU default".

const MiB = 1024 * 1024;

/** Near the limit: worth a look before it runs out. */
const HIGH = 0.9;

const Default = ({ on }: { on: boolean }) =>
  on ? <span className="font-sans text-mut"> default</span> : null;

/** Share of the memory limit in use, 0 to 1, or null without a sample. */
export const memoryShare = (s: Service) =>
  s.usage && s.effective_memory_mb
    ? s.usage.memory_bytes / (s.effective_memory_mb * MiB)
    : null;

/** "231M / 512M", or the limit alone when nothing is running. */
export function MemoryUsage({ service: s }: { service: Service }) {
  const limit = s.effective_memory_mb ? size(s.effective_memory_mb * MiB) : "";
  const share = memoryShare(s);
  if (!s.usage) {
    return (
      <span className="text-mut">
        {limit
          ? `${limit} ${s.memory_from_default ? "default" : "limit"}`
          : "no limit"}
      </span>
    );
  }
  return (
    <span>
      <span className={share !== null && share >= HIGH ? "text-warn" : ""}>
        {size(s.usage.memory_bytes)}
      </span>
      {limit && <span className="text-mut"> / {limit}</span>}
      <Default on={s.memory_from_default} />
    </span>
  );
}

/** "12% of 1 CPU": usage as a share of the CPU limit. */
export function CpuUsage({ service: s }: { service: Service }) {
  const limit = s.effective_cpus;
  if (!s.usage) {
    return (
      <span className="text-mut">
        {limit
          ? `${cpus(limit)} ${s.cpus_from_default ? "default" : "limit"}`
          : "no limit"}
      </span>
    );
  }
  // cpu_percent is relative to one core; the limit may be more or less.
  const share = limit ? s.usage.cpu_percent / limit : s.usage.cpu_percent;
  return (
    <span>
      <span className={limit && share >= HIGH * 100 ? "text-warn" : ""}>
        {pct(share)}
      </span>
      <span className="text-mut">
        {limit ? ` of ${cpus(limit)}` : " of a core"}
      </span>
      <Default on={s.cpus_from_default} />
    </span>
  );
}
