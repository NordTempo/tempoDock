import { Fragment, useMemo, useState } from "react";
import type { MemoryPart, ServerUsage } from "../../data/types";
import { pct, plural, size } from "../../ui/format";
import { Panel } from "../../ui/kit";

type Group = MemoryPart["group"];

/** The groups in the order the bar, legend and table show them. */
const groups: { group: Group; label: string; dot: string; title?: string }[] = [
  {
    group: "apps",
    label: "Apps",
    dot: "bg-mem-apps",
    title:
      "Memory apps can't give back. Page cache isn't counted, so services show more under Services.",
  },
  { group: "platform", label: "Platform", dot: "bg-mem-platform" },
  { group: "system", label: "Other system", dot: "bg-mem-system" },
  { group: "kernel", label: "Kernel", dot: "bg-mem-kernel" },
];

/**
 * Where a server's used memory goes: one bar split by group, and a table
 * of the groups that open to show their parts (the server page's Memory
 * row, opened). Nothing until the server reports a breakdown (cgroup v2,
 * a dock that sends one).
 */
export function MemoryBreakdown({ usage }: { usage?: ServerUsage }) {
  const [open, setOpen] = useState<ReadonlySet<Group>>(() => new Set());
  const parts = usage?.memory_parts;
  const byGroup = useMemo(
    () =>
      groups.map((g) => {
        const own = (parts ?? []).filter((p) => p.group === g.group);
        return {
          ...g,
          parts: own,
          bytes: own.reduce((n, p) => n + p.bytes, 0),
        };
      }),
    [parts],
  );
  if (!usage || !parts?.length) return null;
  const total = usage.memory_total_bytes;
  const used = usage.memory_used_bytes;
  const share = (n: number) => (total ? (100 * n) / total : 0);
  const toggle = (g: Group) =>
    setOpen((was) => {
      const next = new Set(was);
      if (!next.delete(g)) next.add(g);
      return next;
    });

  return (
    <div>
      <div className="flex h-2.5 overflow-hidden rounded-tag bg-line">
        {byGroup.map((g) => (
          <i
            key={g.group}
            className={`block h-full ${g.dot}`}
            style={{ width: `${share(g.bytes)}%` }}
          />
        ))}
      </div>
      <div className="mt-2.5 mb-3.5 flex flex-wrap gap-x-5 gap-y-1.5 text-xs">
        {byGroup.map((g) => (
          <span key={g.group}>
            <Dot className={g.dot} />
            {g.label} <span className="font-mono text-fg">{size(g.bytes)}</span>
          </span>
        ))}
        {total > 0 && (
          <span>
            <Dot className="bg-line outline outline-edge" />
            Available{" "}
            <span className="font-mono text-fg">
              {size(Math.max(0, total - used))}
            </span>
          </span>
        )}
      </div>
      <Panel>
        <table>
          <thead>
            <tr>
              <th>Part</th>
              <th className="text-right">Memory</th>
              <th className="text-right">Of used</th>
              <th className="w-full" />
            </tr>
          </thead>
          <tbody>
            {byGroup.map((g) => {
              if (!g.parts.length) return null;
              const opened = open.has(g.group);
              return (
                <Fragment key={g.group}>
                  <tr title={g.title}>
                    <td>
                      <button
                        type="button"
                        aria-expanded={opened}
                        className="hover:text-fg"
                        onClick={() => toggle(g.group)}
                      >
                        <span aria-hidden className="text-faint">
                          {opened ? "▾" : "▸"}
                        </span>{" "}
                        <Dot className={g.dot} />
                        {g.label}
                      </button>
                    </td>
                    <td className="text-right font-mono">{size(g.bytes)}</td>
                    <td className="text-right font-mono text-mut">
                      {used ? pct((100 * g.bytes) / used) : ""}
                    </td>
                    <td className="text-mut">
                      {plural(g.parts.length, "part")}
                    </td>
                  </tr>
                  {opened &&
                    g.parts.map((p) => (
                      <tr key={`${p.name}/${p.environment ?? ""}`}>
                        <td className="pl-9 font-mono text-sec">{p.name}</td>
                        <td className="text-right font-mono text-sec">
                          {size(p.bytes)}
                        </td>
                        <td className="text-right font-mono text-faint">
                          {used ? pct((100 * p.bytes) / used) : ""}
                        </td>
                        <td className="font-mono text-mut">{p.environment}</td>
                      </tr>
                    ))}
                </Fragment>
              );
            })}
          </tbody>
        </table>
      </Panel>
    </div>
  );
}

function Dot({ className }: { className: string }) {
  return (
    <span
      aria-hidden
      className={`mr-1.5 inline-block size-2 rounded-full ${className}`}
    />
  );
}
