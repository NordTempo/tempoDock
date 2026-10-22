import { useId, useState } from "react";
import type { Token, TokenDataAccess } from "../../data/types";
import { Button, ErrorText } from "../../ui/kit";
import { levels, sensitivities, useSetTokenData } from "./policy";

// A token's own database access (ADR 0016 section 4), on the Tokens page.

/** "non-production read, production none, masked", or "policy". */
export const tokenDataText = (d: TokenDataAccess | null | undefined) =>
  d
    ? `non-production ${d.non_production}, production ${d.production}, ${d.sensitive}`
    : "policy";

/** The `dock token create` flags for a token's own data access. */
export const tokenDataFlags = (d: TokenDataAccess | null) =>
  d
    ? `--data-non-production ${d.non_production} --data-production ${d.production} --data-sensitive ${d.sensitive}`
    : "";

const fallback: TokenDataAccess = {
  non_production: "read",
  production: "none",
  sensitive: "masked",
};

/**
 * Follow the policy's tokens rule (null), or the token's own levels per
 * environment kind and sensitive-column visibility.
 */
export function TokenDataFields({
  value,
  onChange,
}: {
  value: TokenDataAccess | null;
  onChange: (next: TokenDataAccess | null) => void;
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
        disabled={!value}
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
          Its own{" "}
          <span className="text-mut">
            never more than a user with its scope, or than its creator has. On
            production, write acts as propose.
          </span>
        </span>
      </label>
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
    </fieldset>
  );
}

/** Changes an existing token's data access (admin), inline in its row. */
export function TokenDataEditor({
  token,
  onDone,
}: {
  token: Token;
  onDone: () => void;
}) {
  const [value, setValue] = useState<TokenDataAccess | null>(
    token.data ?? null,
  );
  const update = useSetTokenData();
  const cli = value
    ? `dock token update ${token.id} ${tokenDataFlags(value)}`
    : `dock token update ${token.id} --data-clear`;
  return (
    <div className="flex flex-col gap-3 py-2">
      <TokenDataFields value={value} onChange={setValue} />
      <div className="flex flex-wrap items-center gap-2.5">
        <Button
          primary
          small
          disabled={update.isPending}
          onClick={() =>
            update.mutate({ id: token.id, data: value }, { onSuccess: onDone })
          }
        >
          Save data access
        </Button>
        <Button small onClick={onDone}>
          Cancel
        </Button>
        <span className="font-mono text-2xs text-mut">{cli}</span>
        <ErrorText error={update.error} />
      </div>
    </div>
  );
}
