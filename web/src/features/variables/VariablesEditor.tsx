import { useState } from "react";
import { useDiscardChanges, useStageVariable } from "../../data/mutations";
import {
  usePendingChanges,
  useSecretValues,
  useSession,
  useVariables,
} from "../../data/queries";
import type {
  PendingChange,
  Variable,
  VariableRow,
  VariableTarget,
  VariableValue,
} from "../../data/types";
import { Action, ErrorText, Heading } from "../../ui/kit";
import { ApplyBar } from "./ApplyBar";
import { AddVariable, PasteEnv } from "./VariableForms";

export type VariableChange = Extract<
  PendingChange,
  { kind: "variable.set" | "variable.delete" }
>;

const isVariableChange = (c: PendingChange): c is VariableChange =>
  c.kind !== "setting";

/** One table row: a stored variable, a staged addition, or both. */
type Row = {
  name: string;
  scope: Variable["scope"];
  current: VariableRow | null;
  change: VariableChange | undefined;
};

const MASK = "••••••••••••";

/**
 * Variables of a service (with the environment's shared ones below it) or of
 * a whole environment. Edits are staged, highlighted, and applied together.
 */
export function VariablesEditor({
  target,
  environmentName,
  note,
}: {
  target: VariableTarget;
  environmentName: string;
  /** What applying does, shown next to the Apply button. */
  note: string;
}) {
  const variables = useVariables(target);
  const session = useSession();
  const changes = usePendingChanges(target.environmentId)
    .filter(isVariableChange)
    .filter((c) => c.serviceId === null || c.serviceId === target.serviceId);
  const [reveal, setReveal] = useState(false);
  const [form, setForm] = useState<"add" | "paste" | null>(null);
  const secrets = useSecretValues(target, reveal);
  const isAdmin = session?.role === "admin";
  const rows = buildRows(variables, changes);

  return (
    <div className="px-5">
      <Heading
        note={environmentName}
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
              {reveal ? "Hide secrets" : "Reveal secrets"}
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
      <ErrorText error={secrets.error} />
      {form === "add" && (
        <AddVariable target={target} onDone={() => setForm(null)} />
      )}
      {form === "paste" && (
        <PasteEnv
          target={target}
          existing={variables}
          onDone={() => setForm(null)}
        />
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
          {rows.map((row) => (
            <VariableTableRow
              key={`${row.scope}:${row.name}`}
              row={row}
              target={target}
              revealed={
                row.current ? secrets.data?.[row.current.id] : undefined
              }
              shared={!!target.serviceId && row.scope === "environment"}
            />
          ))}
        </tbody>
      </table>
      {rows.length === 0 && <p className="py-2 text-dim">No variables.</p>}

      <ApplyBar
        environmentId={target.environmentId}
        changeIds={changes.map((c) => c.id)}
        note={note}
      />
    </div>
  );
}

/** Service variables first, then the environment's; staged additions inline. */
function buildRows(variables: VariableRow[], changes: VariableChange[]) {
  const rows: Row[] = variables.map((v) => ({
    name: v.name,
    scope: v.scope,
    current: v,
    change: changes.find((c) => c.name === v.name && c.scope === v.scope),
  }));
  for (const change of changes) {
    const exists = rows.some(
      (r) => r.name === change.name && r.scope === change.scope,
    );
    if (!exists) {
      rows.push({
        name: change.name,
        scope: change.scope,
        current: null,
        change,
      });
    }
  }
  const order = (r: Row) => (r.scope === "service" ? 0 : 1);
  return rows.sort((a, b) => order(a) - order(b));
}

function VariableTableRow({
  row,
  target,
  revealed,
  shared,
}: {
  row: Row;
  target: VariableTarget;
  revealed: string | undefined;
  /** An environment variable shown on a service page. */
  shared: boolean;
}) {
  const stage = useStageVariable();
  const discard = useDiscardChanges();
  const [editing, setEditing] = useState(false);
  const { current, change } = row;
  const serviceId = row.scope === "service" ? target.serviceId : null;
  const dim = shared ? "text-dim" : "";

  function save(value: string) {
    if (!current) return;
    stage.mutate(
      {
        environmentId: target.environmentId,
        change: {
          kind: "variable.set",
          serviceId,
          name: row.name,
          scope: row.scope,
          before: current.value,
          value,
          secret: current.value.kind === "secret",
        },
      },
      { onSuccess: () => setEditing(false) },
    );
  }

  function remove() {
    if (!current) return;
    stage.mutate({
      environmentId: target.environmentId,
      change: {
        kind: "variable.delete",
        serviceId,
        name: row.name,
        scope: row.scope,
        before: current.value,
      },
    });
  }

  return (
    <tr className={`group ${change ? "staged" : ""}`}>
      <td className={`font-mono text-xs ${dim}`}>{row.name}</td>
      <td className="w-[52%] font-mono text-xs whitespace-normal">
        {editing && current ? (
          <EditValue
            value={current.value}
            onSave={save}
            onCancel={() => setEditing(false)}
          />
        ) : change ? (
          <StagedValue change={change} />
        ) : (
          current && (
            <ValueView
              value={current.value}
              revealed={revealed}
              struck={current.overridden}
              dim={shared}
            />
          )
        )}
        <ErrorText error={stage.error} />
      </td>
      <td className={dim}>
        {row.scope}
        {current?.overrides && (
          <span className="text-xs text-warn"> overrides environment</span>
        )}
      </td>
      <td className="w-px text-right">
        {change ? (
          <span className="flex justify-end gap-3">
            <span className="text-warn">
              {change.kind === "variable.delete"
                ? "removed"
                : change.before
                  ? "edited"
                  : "added"}
            </span>
            <Action
              onClick={() =>
                discard.mutate({
                  environmentId: target.environmentId,
                  changeIds: [change.id],
                })
              }
            >
              undo
            </Action>
          </span>
        ) : (
          !editing && (
            <span className="flex justify-end gap-3 opacity-0 group-hover:opacity-100 focus-within:opacity-100">
              <Action onClick={() => setEditing(true)}>edit</Action>
              <Action onClick={remove}>remove</Action>
            </span>
          )
        )}
      </td>
    </tr>
  );
}

function ValueView({
  value,
  revealed,
  struck,
  dim,
}: {
  value: VariableValue;
  revealed?: string;
  struck?: boolean;
  dim?: boolean;
}) {
  const strike = struck ? "line-through" : "";
  switch (value.kind) {
    case "plain":
      return (
        <span className={`${strike} ${dim || struck ? "text-dim" : ""}`}>
          {value.value}
        </span>
      );
    case "secret":
      return revealed !== undefined ? (
        <span className={strike}>{revealed}</span>
      ) : (
        <span className={`text-dim ${strike}`}>{MASK} secret</span>
      );
    case "reference":
      return (
        <span className={strike}>
          <span className={dim ? "text-dim" : "text-run"}>
            {value.template}
          </span>{" "}
          <span className="text-dim">→ {value.resolved}</span>
        </span>
      );
  }
}

function plainText(value: VariableValue) {
  switch (value.kind) {
    case "plain":
      return value.value;
    case "secret":
      return MASK;
    case "reference":
      return value.template;
  }
}

function StagedValue({ change }: { change: VariableChange }) {
  if (change.kind === "variable.delete") {
    return (
      <span className="text-bad line-through">{plainText(change.before)}</span>
    );
  }
  return (
    <>
      {change.before && (
        <>
          <span className="text-bad line-through">
            {plainText(change.before)}
          </span>{" "}
        </>
      )}
      <span className="text-ok">{change.secret ? MASK : change.value}</span>
    </>
  );
}

function EditValue({
  value,
  onSave,
  onCancel,
}: {
  value: VariableValue;
  onSave: (value: string) => void;
  onCancel: () => void;
}) {
  const [draft, setDraft] = useState(
    value.kind === "secret" ? "" : plainText(value),
  );
  return (
    <input
      // biome-ignore lint/a11y/noAutofocus: opened by clicking edit
      autoFocus
      value={draft}
      type={value.kind === "secret" ? "password" : "text"}
      placeholder={value.kind === "secret" ? "new secret value" : undefined}
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
