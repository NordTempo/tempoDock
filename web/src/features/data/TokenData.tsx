import { useId, useState } from "react";
import { useAction } from "../../data/action";
import { Button, ErrorText } from "../../ui/kit";
import { levels, sensitivities } from "./policy";
import type { DataTokenAccess } from "./types";

// A token's own database access (ADR 0016 section 4), on Dock's and
// tempoBase's Tokens pages.

type Access = NonNullable<DataTokenAccess>;

/** "non-production read, production none, masked", or "policy". */
export const tokenDataText = (d: DataTokenAccess | undefined) =>
  d
    ? `non-production ${d.non_production}, production ${d.production}, ${d.sensitive}`
    : "policy";

/** The `dock token create` flags for a token's own data access. */
export const tokenDataFlags = (d: DataTokenAccess) =>
  d
    ? `--data-non-production ${d.non_production} --data-production ${d.production} --data-sensitive ${d.sensitive}`
    : "";

const fallback: Access = {
  non_production: "read",
  production: "none",
  sensitive: "masked",
};

/**
 * Follow the policy's tokens rule (null), or the token's own levels per
 * environment kind and sensitive-column visibility. `cap` says what caps
 * a token's own access on this host.
 */
export function TokenDataFields({
  value,
  onChange,
  cap,
}: {
  value: DataTokenAccess;
  onChange: (next: DataTokenAccess) => void;
  cap: string;
}) {
  const own = value ?? fallback;
  const name = useId();
  const select = (
    label: string,
    current: string,
    options: readonly string[],
    set: (v: string) => void,
  ) => (
    <label className="flex flex-col gap-1.5">
      <span className="text-mut">{label}</span>
      <select
        className="field py-1"
        value={current}
        onChange={(e) => set(e.target.value)}
      >
        {options.map((o) => (
          <option key={o}>{o}</option>
        ))}
      </select>
    </label>
  );
  return (
    <fieldset className="flex flex-col gap-2">
      <legend className="mb-1.5">Data access</legend>
      <label className="flex items-baseline gap-2">
        <input
          type="radio"
          name={name}
          checked={!value}
          onChange={() => onChange(null)}
        />
        <span>
          Follow the data policy{" "}
          <span className="text-mut">its tokens rule, none by default</span>
        </span>
      </label>
      <label className="flex items-baseline gap-2">
        <input
          type="radio"
          name={name}
          checked={!!value}
          onChange={() => onChange(own)}
        />
        <span>
          Its own <span className="text-mut">{cap}</span>
        </span>
      </label>
      {/* The levels only once "Its own" is chosen. */}
      {value && (
        <div className="flex flex-wrap gap-3 pl-6">
          {select("Non-production", own.non_production, levels, (v) => {
            const level = levels.find((l) => l === v);
            if (level) onChange({ ...own, non_production: level });
          })}
          {select("Production", own.production, levels, (v) => {
            const level = levels.find((l) => l === v);
            if (level) onChange({ ...own, production: level });
          })}
          {select("Sensitive columns", own.sensitive, sensitivities, (v) => {
            const sensitive = sensitivities.find((s) => s === v);
            if (sensitive) onChange({ ...own, sensitive });
          })}
        </div>
      )}
    </fieldset>
  );
}

/**
 * Changes an existing token's data access, inline in its row: `onSave`
 * sends it (PATCH /tokens/{id}), `cli` is the command for a value (none
 * where the host has no CLI).
 */
export function TokenDataEditor({
  data,
  onSave,
  onDone,
  cap,
  cli,
}: {
  data: DataTokenAccess | undefined;
  onSave: (data: DataTokenAccess) => Promise<unknown>;
  onDone: () => void;
  cap: string;
  cli?: (data: DataTokenAccess) => string;
}) {
  const [value, setValue] = useState<DataTokenAccess>(data ?? null);
  const save = useAction(
    async (access: DataTokenAccess) => {
      await onSave(access);
      onDone();
    },
    () => [],
  );
  return (
    <div className="flex flex-col gap-3 py-2">
      <TokenDataFields value={value} onChange={setValue} cap={cap} />
      <div className="flex flex-wrap items-center gap-2.5">
        <Button
          primary
          small
          disabled={save.isPending}
          onClick={() => save.mutate(value)}
        >
          Save data access
        </Button>
        <Button small onClick={onDone}>
          Cancel
        </Button>
        {cli && (
          <span className="font-mono text-2xs text-mut">{cli(value)}</span>
        )}
        <ErrorText error={save.error} />
      </div>
    </div>
  );
}
