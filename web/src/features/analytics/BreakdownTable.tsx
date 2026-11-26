import { Panel } from "../../ui/kit";
import type { AnalyticsRow } from "./data";
import { Labeled } from "./icons";
import { count, shown } from "./parts";

/** What a breakdown's count column counts. */
const counted: Record<string, string> = {
  page: "Views",
  entry_page: "Sessions",
  exit_page: "Sessions",
};

/**
 * One dimension's values with their visitors, a bar behind each; clicking
 * one filters by it. The Overview's panels and the Globe's country list.
 * With `onHover`, the row under the pointer is reported and `hover` lit.
 */
export function BreakdownTable({
  dimension,
  label,
  rows,
  error,
  onPick,
  hover,
  onHover,
}: {
  dimension: string;
  /** The value column's name, for screen readers. */
  label: string;
  /** Undefined while loading. */
  rows: AnalyticsRow[] | null | undefined;
  error?: Error | null;
  onPick: (dimension: string, row: AnalyticsRow) => void;
  hover?: string | null;
  onHover?: (value: string | null) => void;
}) {
  const most = Math.max(1, ...(rows ?? []).map((r) => r.visitors));
  return (
    <Panel>
      <table className="tabular-nums">
        <thead>
          <tr>
            <th className="sr-only">{label}</th>
            <th className="w-20 text-right">Visitors</th>
            <th className="w-20 text-right">
              {counted[dimension] ?? "Events"}
            </th>
          </tr>
        </thead>
        <tbody>
          {rows?.map((r) => (
            <tr
              key={r.value}
              className={r.value === hover ? "bg-hover" : undefined}
              onPointerEnter={onHover && (() => onHover(r.value))}
              onPointerLeave={onHover && (() => onHover(null))}
            >
              <td className="relative max-w-0 truncate">
                <span
                  className="absolute inset-y-1 left-0 rounded-xs bg-info/8"
                  style={{ width: `${(r.visitors / most) * 100}%` }}
                />
                <button
                  type="button"
                  className="relative -my-[3px] min-h-[24px] min-w-[24px] max-w-full truncate text-left"
                  title={`Filter by ${shown(dimension, r)}`}
                  onClick={() => onPick(dimension, r)}
                >
                  <Labeled dimension={dimension} value={r.value}>
                    {shown(dimension, r)}
                  </Labeled>
                </button>
              </td>
              <td className="text-right">{count(r.visitors)}</td>
              <td className="text-right text-mut">{count(r.count)}</td>
            </tr>
          ))}
          {rows?.length === 0 && (
            <tr>
              <td colSpan={3} className="text-mut">
                None in this range.
              </td>
            </tr>
          )}
          {!rows && (
            <tr>
              <td colSpan={3} className="text-mut">
                {error ? error.message : "Loading…"}
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </Panel>
  );
}
