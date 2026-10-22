import { useState } from "react";
import { filterText, opLabels } from "./cells";
import { Btn } from "./kit";
import type { DataColumn, DataFilter, DataFilterOp } from "./types";

const ops = Object.keys(opLabels) as DataFilterOp[];
const noValue = (op: DataFilterOp) => op === "is_null" || op === "not_null";

/**
 * Structured filters (column, op, value; ANDed) and the sort as chips, each
 * with × to remove it, and the form to add a filter while `adding`.
 * Renders nothing when there is nothing to show.
 */
export function FilterBar({
  columns,
  filters,
  order,
  adding,
  onAdding,
  onFilters,
  onOrder,
}: {
  columns: readonly DataColumn[];
  filters: readonly DataFilter[];
  order?: string;
  adding: boolean;
  onAdding: (adding: boolean) => void;
  onFilters: (next: DataFilter[]) => void;
  onOrder: (next: string | undefined) => void;
}) {
  if (!filters.length && !order && !adding) return null;
  return (
    <div className="flex flex-wrap items-center gap-1.5 px-5 pt-3">
      {filters.map((f, i) => (
        <span key={`${f.column}${f.op}${i}`} className="g-chip on">
          {filterText(f)}
          <button
            type="button"
            className="text-(--g-mut) hover:text-(--g-text)"
            aria-label={`Remove filter ${filterText(f)}`}
            onClick={() => onFilters(filters.filter((_, j) => j !== i))}
          >
            ×
          </button>
        </span>
      ))}
      {order && (
        <span className="g-chip">
          {order.replace(/^-/, "")} {order.startsWith("-") ? "↓" : "↑"}
          <button
            type="button"
            className="text-(--g-mut) hover:text-(--g-text)"
            aria-label="Remove sort"
            onClick={() => onOrder(undefined)}
          >
            ×
          </button>
        </span>
      )}
      {adding && (
        <FilterForm
          columns={columns}
          onAdd={(f) => {
            onFilters([...filters, f]);
            onAdding(false);
          }}
          onCancel={() => onAdding(false)}
        />
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
      className="flex flex-wrap items-center gap-1.5"
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
        className="g-field h-7 text-xs"
        value={column}
        onChange={(e) => setColumn(e.target.value)}
      >
        {columns.map((c) => (
          <option key={c.name}>{c.name}</option>
        ))}
      </select>
      <select
        aria-label="Operator"
        className="g-field h-7 text-xs"
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
          className="g-field h-7 w-44 font-mono text-xs"
          placeholder={op === "in" ? "a, b, c" : "value"}
          value={value}
          onChange={(e) => setValue(e.target.value)}
        />
      )}
      <Btn type="submit" small>
        Add
      </Btn>
      <Btn small ghost onClick={onCancel}>
        Cancel
      </Btn>
    </form>
  );
}
