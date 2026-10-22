import { useState } from "react";
import { useShortcuts } from "../../shell/keys";
import { filterText, opLabels } from "./cells";
import type { DataColumn, DataFilter, DataFilterOp } from "./types";

const ops = Object.keys(opLabels) as DataFilterOp[];
const noValue = (op: DataFilterOp) => op === "is_null" || op === "not_null";

/**
 * Structured filters (column, op, value; ANDed) and the sort, as one line
 * of text with × to remove each. `f` opens the form to add a filter.
 */
export function FilterBar({
  columns,
  filters,
  order,
  onFilters,
  onOrder,
}: {
  columns: readonly DataColumn[];
  filters: readonly DataFilter[];
  order?: string;
  onFilters: (next: DataFilter[]) => void;
  onOrder: (next: string | undefined) => void;
}) {
  const [adding, setAdding] = useState(false);
  useShortcuts([{ key: "f", label: "filter", run: () => setAdding(true) }]);

  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5 font-mono text-xs">
      {filters.map((f, i) => (
        <span key={`${f.column}${f.op}${i}`} className="whitespace-nowrap">
          <span className="text-mut">filter:</span> {filterText(f)}{" "}
          <button
            type="button"
            className="lnk"
            aria-label={`Remove filter ${filterText(f)}`}
            onClick={() => onFilters(filters.filter((_, j) => j !== i))}
          >
            ×
          </button>
        </span>
      ))}
      {order && (
        <span className="whitespace-nowrap">
          <span className="text-mut">order:</span> {order.replace(/^-/, "")}{" "}
          {order.startsWith("-") ? "↓" : "↑"}{" "}
          <button
            type="button"
            className="lnk"
            aria-label="Remove sort"
            onClick={() => onOrder(undefined)}
          >
            ×
          </button>
        </span>
      )}
      {adding ? (
        <FilterForm
          columns={columns}
          onAdd={(f) => {
            onFilters([...filters, f]);
            setAdding(false);
          }}
          onCancel={() => setAdding(false)}
        />
      ) : (
        <button type="button" className="lnk" onClick={() => setAdding(true)}>
          + filter <kbd>F</kbd>
        </button>
      )}
    </div>
  );
}

function FilterForm({
  columns,
  onAdd,
  onCancel,
}: {
  columns: readonly DataColumn[];
  onAdd: (f: DataFilter) => void;
  onCancel: () => void;
}) {
  const [column, setColumn] = useState(columns[0]?.name ?? "");
  const [op, setOp] = useState<DataFilterOp>("eq");
  const [value, setValue] = useState("");

  const submit = () => {
    if (!column) return;
    if (noValue(op)) onAdd({ column, op });
    else if (op === "in") {
      onAdd({ column, op, values: value.split(",").map((v) => v.trim()) });
    } else onAdd({ column, op, value });
  };

  return (
    <form
      className="flex items-center gap-1.5"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
      onKeyDown={(e) => e.key === "Escape" && onCancel()}
    >
      <select
        // biome-ignore lint/a11y/noAutofocus: the form opens on request
        autoFocus
        aria-label="Column"
        className="field py-0.5 text-xs"
        value={column}
        onChange={(e) => setColumn(e.target.value)}
      >
        {columns.map((c) => (
          <option key={c.name}>{c.name}</option>
        ))}
      </select>
      <select
        aria-label="Operator"
        className="field py-0.5 text-xs"
        value={op}
        onChange={(e) => setOp(ops.find((o) => o === e.target.value) ?? "eq")}
      >
        {ops.map((o) => (
          <option key={o} value={o}>
            {opLabels[o]}
          </option>
        ))}
      </select>
      {!noValue(op) && (
        <input
          aria-label="Value"
          className="field w-40 py-0.5 text-xs"
          placeholder={op === "in" ? "a, b, c" : "value"}
          value={value}
          onChange={(e) => setValue(e.target.value)}
        />
      )}
      <button type="submit" className="btn sm">
        Add
      </button>
      <button type="button" className="lnk" onClick={onCancel}>
        Cancel
      </button>
    </form>
  );
}
