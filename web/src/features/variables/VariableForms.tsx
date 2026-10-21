import { useState } from "react";
import { useStageVariable } from "../../data/mutations";
import type { Variable, VariableRow, VariableTarget } from "../../data/types";
import { Action, Button, ErrorText, inputClass } from "../../ui/kit";
import { parseDotenv } from "./dotenv";

type Scope = Variable["scope"];

/** Scope picker; environment pages only have one scope. */
function ScopeSelect({
  target,
  value,
  onChange,
}: {
  target: VariableTarget;
  value: Scope;
  onChange: (scope: Scope) => void;
}) {
  if (!target.serviceId) return <span className="text-dim">environment</span>;
  return (
    <select
      value={value}
      aria-label="Scope"
      onChange={(e) =>
        onChange(e.target.value === "environment" ? "environment" : "service")
      }
      className="border-b border-line2 bg-black text-white outline-none focus:border-white"
    >
      <option value="service">service</option>
      <option value="environment">environment (all services)</option>
    </select>
  );
}

const defaultScope = (t: VariableTarget): Scope =>
  t.serviceId ? "service" : "environment";

export function AddVariable({
  target,
  onDone,
}: {
  target: VariableTarget;
  onDone: () => void;
}) {
  const stage = useStageVariable();
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [scope, setScope] = useState(defaultScope(target));
  const [secret, setSecret] = useState(false);

  return (
    <form
      className="flex flex-wrap items-baseline gap-3 py-2"
      onSubmit={(e) => {
        e.preventDefault();
        stage.mutate(
          {
            environmentId: target.environmentId,
            change: {
              kind: "variable.set",
              serviceId: scope === "service" ? target.serviceId : null,
              name: name.trim(),
              scope,
              before: null,
              value,
              secret,
            },
          },
          { onSuccess: onDone },
        );
      }}
    >
      <input
        // biome-ignore lint/a11y/noAutofocus: opened on request
        autoFocus
        value={name}
        onChange={(e) => setName(e.target.value)}
        placeholder="NAME"
        aria-label="Name"
        className={`${inputClass} w-56 font-mono text-xs`}
      />
      <input
        value={value}
        type={secret ? "password" : "text"}
        onChange={(e) => setValue(e.target.value)}
        placeholder="value or ${{service.VAR}}"
        aria-label="Value"
        className={`${inputClass} w-80 font-mono text-xs`}
      />
      <ScopeSelect target={target} value={scope} onChange={setScope} />
      <label className="flex items-baseline gap-1.5 text-dim">
        <input
          type="checkbox"
          checked={secret}
          onChange={(e) => setSecret(e.target.checked)}
        />
        secret
      </label>
      <Button primary type="submit" disabled={stage.isPending}>
        Stage
      </Button>
      <Action onClick={onDone}>Cancel</Action>
      <ErrorText error={stage.error} />
    </form>
  );
}

/** Bulk paste of a .env file; every line becomes a staged change. */
export function PasteEnv({
  target,
  existing,
  onDone,
}: {
  target: VariableTarget;
  existing: VariableRow[];
  onDone: () => void;
}) {
  const stage = useStageVariable();
  const [text, setText] = useState("");
  const [scope, setScope] = useState(defaultScope(target));
  const [secret, setSecret] = useState(false);
  const parsed = parseDotenv(text);

  async function stageAll() {
    for (const { name, value } of parsed) {
      const before =
        existing.find((v) => v.name === name && v.scope === scope)?.value ??
        null;
      await stage.mutateAsync({
        environmentId: target.environmentId,
        change: {
          kind: "variable.set",
          serviceId: scope === "service" ? target.serviceId : null,
          name,
          scope,
          before,
          value,
          secret,
        },
      });
    }
    onDone();
  }

  return (
    <div className="max-w-[760px] py-2">
      <textarea
        // biome-ignore lint/a11y/noAutofocus: opened on request
        autoFocus
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder={"DATABASE_URL=postgres://...\nLOG_LEVEL=info"}
        aria-label=".env contents"
        rows={6}
        className="w-full resize-y border border-line2 bg-black p-2 font-mono text-xs outline-none focus:border-white placeholder:text-faint"
      />
      <div className="flex flex-wrap items-baseline gap-3 pt-1.5">
        <ScopeSelect target={target} value={scope} onChange={setScope} />
        <label className="flex items-baseline gap-1.5 text-dim">
          <input
            type="checkbox"
            checked={secret}
            onChange={(e) => setSecret(e.target.checked)}
          />
          all secret
        </label>
        <Button
          primary
          disabled={parsed.length === 0 || stage.isPending}
          onClick={() => stageAll().catch(() => {})}
        >
          Stage {parsed.length} {parsed.length === 1 ? "variable" : "variables"}
        </Button>
        <Action onClick={onDone}>Cancel</Action>
        <ErrorText error={stage.error} />
      </div>
    </div>
  );
}
