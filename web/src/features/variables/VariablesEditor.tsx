import { useState } from "react";
import { useStageVariables, useUndoVariable } from "../../data/mutations";
import {
  useIsAdmin,
  useRevealedChanges,
  useRevealedVariables,
  useVariables,
} from "../../data/queries";
import type { EnvRef, Variable } from "../../data/types";
import { Action, ErrorText, Heading } from "../../ui/kit";
import { ApplyBar } from "./ApplyBar";
import { AddVariable, PasteEnv } from "./VariableForms";

const MASK = "••••••••••••";

/** Identifies a variable across lists: scope, owning service and key. */
const idOf = (v: { key: string; scope: string; service?: string }) =>
  `${v.scope}:${v.service ?? ""}:${v.key}`;

type Values = { live?: string; old?: string; new?: string };

/**
 * Variables of a service (with the environment's shared ones below it) or
 * an environment's shared variables. Values stay hidden unless an admin
 * reveals them. Edits are staged, highlighted, and applied together.
 */
export function VariablesEditor({
  env,
  service,
  note,
}: {
  env: EnvRef;
  /** The service whose page this is; null on the environment page. */
  service: string | null;
  note: string;
}) {
  const all = useVariables(env, service);
  const isAdmin = useIsAdmin();
  const [reveal, setReveal] = useState(false);
  const [form, setForm] = useState<"add" | "paste" | null>(null);
  const revealed = useRevealedVariables(env, service, reveal);
  const revealedChanges = useRevealedChanges(env, reveal);

  // Without a service the API lists every service's variables too.
  const variables = service
    ? all
    : all.filter((v) => v.scope === "environment");
  const order = (v: Variable) => (v.scope === "service" ? 0 : 1);
  const rows = [...variables].sort(
    (a, b) => order(a) - order(b) || a.key.localeCompare(b.key),
  );
  const serviceKeys = new Set(
    variables.filter((v) => v.scope === "service").map((v) => v.key),
  );

  const values = new Map<string, Values>();
  for (const v of revealed.data ?? []) {
    values.set(idOf(v), { live: v.value });
  }
  for (const c of revealedChanges.data?.variables ?? []) {
    values.set(idOf(c), { ...values.get(idOf(c)), old: c.old, new: c.new });
  }

  return (
    <div className="px-5">
      <Heading
        note={note}
        actions={
          <>
            <Action onClick={() => setForm(form === "paste" ? null : "paste")}>
              Paste .env
            </Action>
            <Action
              disabled={!isAdmin}
              title={isAdmin ? undefined : "Requires admin"}
              onClick={() => setReveal(!reveal)}
            >
              {reveal ? "Hide values" : "Reveal values"}
            </Action>
            <button
              type="button"
              className="hover:underline"
              onClick={() => setForm(form === "add" ? null : "add")}
            >
              Add
            </button>
          </>
        }
      >
        Variables
      </Heading>
      <ErrorText error={revealed.error ?? revealedChanges.error} />
      {form === "add" && (
        <AddVariable env={env} service={service} onDone={() => setForm(null)} />
      )}
      {form === "paste" && (
        <PasteEnv env={env} service={service} onDone={() => setForm(null)} />
      )}

      <table className="tbl">
        <thead>
          <tr>
            <th>Name</th>
            <th>Value</th>
            <th>Scope</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {rows.map((variable) => (
            <VariableRow
              key={idOf(variable)}
              env={env}
              variable={variable}
              values={reveal ? values.get(idOf(variable)) : undefined}
              shared={!!service && variable.scope === "environment"}
              overrides={
                !!service &&
                variable.scope === "service" &&
                variables.some(
                  (v) => v.scope === "environment" && v.key === variable.key,
                )
              }
              overridden={
                variable.scope === "environment" &&
                serviceKeys.has(variable.key)
              }
            />
          ))}
        </tbody>
      </table>
      {rows.length === 0 && <p className="py-2 text-dim">No variables.</p>}

      <ApplyBar env={env} />
    </div>
  );
}

const pendingLabel = { add: "added", update: "edited", remove: "removed" };

function VariableRow({
  env,
  variable,
  values,
  shared,
  overrides,
  overridden,
}: {
  env: EnvRef;
  variable: Variable;
  /** Revealed values, when an admin asked for them. */
  values: Values | undefined;
  /** An environment variable shown on a service page. */
  shared: boolean;
  /** A service variable that replaces a shared one of the same name. */
  overrides: boolean;
  /** A shared variable replaced by this service's own. */
  overridden: boolean;
}) {
  const stage = useStageVariables();
  const undo = useUndoVariable();
  const isAdmin = useIsAdmin();
  const [editing, setEditing] = useState(false);
  const { key, pending } = variable;
  const service =
    variable.scope === "service" ? (variable.service ?? null) : null;
  const dim = shared ? "text-dim" : "";
  // Undoing an edit or removal restores the live value, which needs admin.
  const canUndo = pending === "add" || isAdmin;

  const save = (value: string) =>
    stage.mutate(
      { env, service, set: { [key]: value } },
      { onSuccess: () => setEditing(false) },
    );

  return (
    <tr className={`group ${pending ? "staged" : ""}`}>
      <td className={`font-mono text-xs ${dim}`}>{key}</td>
      <td className="w-[52%] font-mono text-xs whitespace-normal">
        {editing ? (
          <EditValue
            initial={values?.new ?? values?.live ?? ""}
            onSave={save}
            onCancel={() => setEditing(false)}
          />
        ) : (
          <ValueView
            pending={pending}
            values={values}
            struck={overridden}
            dim={shared}
          />
        )}
        <ErrorText error={stage.error ?? undo.error} />
      </td>
      <td className={dim}>
        {variable.scope === "environment" ? "shared" : "service"}
        {overrides && (
          <span className="text-xs text-warn"> overrides shared</span>
        )}
      </td>
      <td className="w-px text-right">
        {pending ? (
          <span className="flex justify-end gap-3">
            <span className="text-warn">{pendingLabel[pending]}</span>
            <Action
              disabled={!canUndo || undo.isPending}
              title={
                canUndo
                  ? undefined
                  : "Restoring the live value requires admin; Discard all drops every staged change"
              }
              onClick={() => undo.mutate({ env, variable })}
            >
              undo
            </Action>
          </span>
        ) : (
          !editing && (
            <span className="flex justify-end gap-3 opacity-0 group-hover:opacity-100 focus-within:opacity-100">
              <Action onClick={() => setEditing(true)}>edit</Action>
              <Action
                disabled={stage.isPending}
                onClick={() => stage.mutate({ env, service, unset: [key] })}
              >
                remove
              </Action>
            </span>
          )
        )}
      </td>
    </tr>
  );
}

/** A value, with `${{service.KEY}}` references highlighted. */
function Text({ value, dim }: { value: string; dim?: boolean }) {
  const parts = value.split(/(\$\{\{[^}]*\}\})/);
  return parts.map((part, i) =>
    part.startsWith("${{") ? (
      <span key={i} className={dim ? "text-dim" : "text-run"}>
        {part}
      </span>
    ) : (
      part
    ),
  );
}

function ValueView({
  pending,
  values,
  struck,
  dim,
}: {
  pending: Variable["pending"];
  values: Values | undefined;
  struck: boolean;
  dim: boolean;
}) {
  const strike = struck ? "line-through text-dim" : dim ? "text-dim" : "";
  const masked = <span className="text-dim">{MASK}</span>;
  switch (pending) {
    case "add":
      return (
        <span className="text-ok">
          {values?.new === undefined ? MASK : <Text value={values.new} />}
        </span>
      );
    case "update":
      return (
        <>
          <span className="text-bad line-through">{values?.old ?? MASK}</span>{" "}
          <span className="text-ok">
            {values?.new === undefined ? MASK : <Text value={values.new} />}
          </span>
        </>
      );
    case "remove":
      return (
        <span className="text-bad line-through">{values?.old ?? MASK}</span>
      );
    case undefined:
      return values?.live === undefined ? (
        masked
      ) : (
        <span className={strike}>
          <Text value={values.live} dim={dim || struck} />
        </span>
      );
  }
}

function EditValue({
  initial,
  onSave,
  onCancel,
}: {
  initial: string;
  onSave: (value: string) => void;
  onCancel: () => void;
}) {
  const [draft, setDraft] = useState(initial);
  return (
    <input
      // biome-ignore lint/a11y/noAutofocus: opened by clicking edit
      autoFocus
      value={draft}
      placeholder="new value"
      aria-label="Value"
      onChange={(e) => setDraft(e.target.value)}
      onKeyDown={(e) => {
        if (e.key === "Enter") onSave(draft);
        if (e.key === "Escape") onCancel();
      }}
      className="w-full border-b border-white bg-black outline-none placeholder:text-faint"
    />
  );
}
