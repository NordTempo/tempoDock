import { useState } from "react";
import type { components } from "../../api/schema";
import { Act, ErrorText } from "../../ui/kit";
import { useRoles } from "./data";

// What agents may use (ADR 0080, phase 6), as a matrix: a person's agent
// setting on their account page, the organization's agent rules in its
// settings.

type Setting = components["schemas"]["AgentSetting"];
const kinds = ["production", "other", "previews"] as const;

/**
 * The editable matrix: an area role per row, an environment kind per
 * column, each a level (off, its own, all) and ask. Save sends the
 * changed cells; cli is each one's command.
 */
export function AgentMatrix({
  saved,
  pending,
  error,
  cli: cliOf,
  onSave,
}: {
  saved: Setting[];
  pending: boolean;
  error: Error | null;
  cli: (c: Setting) => string;
  onSave: (changed: Setting[]) => void;
}) {
  const roles = useRoles();
  const [cells, setCells] = useState<Setting[]>(saved);
  const levelsOf = (area: string) => {
    const r = roles.find((x) => x.key === area);
    return (r?.levels ?? []).map((l) => l.name || "yes").filter(Boolean);
  };
  const changed = cells.filter((c, i) => {
    const s = saved[i];
    return s && (s.level !== c.level || !!s.ask !== !!c.ask);
  });
  const cli = changed.map(cliOf).join("; ");
  const update = (i: number, patch: Partial<Setting>) =>
    setCells(cells.map((c, j) => (j === i ? { ...c, ...patch } : c)));
  const areas = [...new Set(cells.map((c) => c.area))];
  return (
    <>
      <table>
        <thead>
          <tr>
            <th>Area</th>
            {kinds.map((k) => (
              <th key={k}>{k}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {areas.map((area) => (
            <tr key={area}>
              <td className="py-1.5">
                {roles.find((r) => r.key === area)?.name ?? area}
              </td>
              {kinds.map((k) => {
                const i = cells.findIndex(
                  (c) => c.area === area && c.kind === k,
                );
                const c = cells[i];
                if (!c) return <td key={k} />;
                return (
                  <td key={k} className="py-1.5">
                    <span className="flex items-center gap-2">
                      <select
                        className="field w-28"
                        aria-label={`${area} in ${k}`}
                        value={c.level}
                        onChange={(e) => update(i, { level: e.target.value })}
                      >
                        <option value="off">off</option>
                        {levelsOf(area).map((l) => (
                          <option key={l} value={l === "yes" ? "" : l}>
                            {l}
                          </option>
                        ))}
                        <option value="all">all</option>
                      </select>
                      <label className="flex items-center gap-1 text-mut">
                        <input
                          type="checkbox"
                          className="accent-fg"
                          checked={!!c.ask}
                          onChange={(e) => update(i, { ask: e.target.checked })}
                        />
                        ask
                      </label>
                    </span>
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
      <div>
        <Act
          primary
          cli={cli || "dock agent set AREA=LEVEL --kinds KIND"}
          disabled={changed.length === 0 || pending}
          onClick={() => onSave(changed)}
        >
          Save
        </Act>
      </div>
      {error && <ErrorText error={error} />}
    </>
  );
}
