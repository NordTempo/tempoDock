import { useState } from "react";
import { useStageVariables } from "../../data/mutations";
import type { EnvRef } from "../../data/types";
import { Action, Button, ErrorText, inputClass } from "../../ui/kit";
import { parseDotenv } from "./dotenv";

type FormProps = {
  env: EnvRef;
  /** The page's service; null on the environment page (shared only). */
  service: string | null;
  onDone: () => void;
};

/** Service or shared scope; environment pages only have shared. */
function useScope(service: string | null) {
  const [shared, setShared] = useState(service === null);
  const select = service ? (
    <select
      value={shared ? "shared" : "service"}
      aria-label="Scope"
      onChange={(e) => setShared(e.target.value === "shared")}
      className="border-b border-line2 bg-black text-white outline-none focus:border-white"
    >
      <option value="service">{service} only</option>
      <option value="shared">shared (all services)</option>
    </select>
  ) : (
    <span className="text-dim">shared</span>
  );
  return { target: shared ? null : service, select };
}

export function AddVariable({ env, service, onDone }: FormProps) {
  const stage = useStageVariables();
  const scope = useScope(service);
  const [name, setName] = useState("");
  const [value, setValue] = useState("");

  return (
    <form
      className="flex flex-wrap items-baseline gap-3 py-2"
      onSubmit={(e) => {
        e.preventDefault();
        stage.mutate(
          { env, service: scope.target, set: { [name.trim()]: value } },
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
        onChange={(e) => setValue(e.target.value)}
        placeholder="value or ${{service.VAR}}"
        aria-label="Value"
        className={`${inputClass} w-80 font-mono text-xs`}
      />
      {scope.select}
      <Button primary type="submit" disabled={stage.isPending}>
        Stage
      </Button>
      <Action onClick={onDone}>Cancel</Action>
      <ErrorText error={stage.error} />
    </form>
  );
}

/** Bulk paste of a .env file, staged as one change set. */
export function PasteEnv({ env, service, onDone }: FormProps) {
  const stage = useStageVariables();
  const scope = useScope(service);
  const [text, setText] = useState("");
  const parsed = parseDotenv(text);

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
        {scope.select}
        <Button
          primary
          disabled={parsed.length === 0 || stage.isPending}
          onClick={() =>
            stage.mutate(
              {
                env,
                service: scope.target,
                set: Object.fromEntries(parsed.map((v) => [v.name, v.value])),
              },
              { onSuccess: onDone },
            )
          }
        >
          Stage {parsed.length} {parsed.length === 1 ? "variable" : "variables"}
        </Button>
        <Action onClick={onDone}>Cancel</Action>
        <ErrorText error={stage.error} />
      </div>
    </div>
  );
}
