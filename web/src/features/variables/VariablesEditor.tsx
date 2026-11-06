import { Link } from "@tanstack/react-router";
import { type ReactNode, type RefObject, useRef, useState } from "react";
import { useDiscardChanges, useStageVariables } from "../../data/mutations";
import {
  useEnvironment,
  useIsAdmin,
  useRevealedChanges,
  useRevealedVariables,
  useVariables,
} from "../../data/queries";
import { typedValue } from "../../data/typed";
import type { EnvRef, Variable } from "../../data/types";
import { useShortcuts } from "../../shell/keys";
import { useOrg } from "../../shell/location";
import { plural } from "../../ui/format";
import {
  Act,
  Button,
  Check,
  ErrorText,
  Glyph,
  SectionHead,
} from "../../ui/kit";
import { serviceState } from "../../ui/status";
import { ApplyBox } from "./ApplyBox";
import { parseDotenv } from "./dotenv";
import { hasReference, maskUrlPasswords, resolve } from "./references";

const MASK = "••••••••••••";

/**
 * A staged line's new value when this session typed it (data/typed.ts),
 * so it can be checked before Apply; the rest stays masked.
 */
function typedOnly(env: EnvRef, v: Variable) {
  if (v.pending !== "add" && v.pending !== "update") return undefined;
  const value = typedValue(
    env,
    v.scope === "service" ? v.service : null,
    v.key,
  );
  return value === undefined ? undefined : { new: value };
}

/** The CLI's flags for a service's variables, or the shared ones. */
const scopeFlags = (env: EnvRef, service: string | null) =>
  `${service ? `-s ${service}` : "--shared"} -e ${env.environment}`;

/** Identifies a variable across lists: scope, owning service and key. */
const idOf = (v: { key: string; scope: string; service?: string }) =>
  `${v.scope}:${v.service ?? ""}:${v.key}`;

/**
 * An environment's variables as an .env-like editor: pick what to look at
 * (the environment's shared lines, or what one service sees at runtime),
 * edit lines in place, paste a .env, and apply staged lines together.
 * Values stay masked until an admin reveals them. footer goes under
 * them: the environment's other settings (log drains). It sits under
 * another page's header: the project's settings for the shared lines, a
 * service's for its Variables tab (ADR 0059); the scope list switches
 * between them.
 */
export function VariablesEditor({
  env,
  service,
  footer,
}: {
  env: EnvRef;
  /** The service whose runtime view to show; null for shared lines only. */
  service: string | null;
  footer?: ReactNode;
}) {
  const org = useOrg();
  const all = useVariables(env, null);
  const { services } = useEnvironment(env);
  const isAdmin = useIsAdmin();
  const [revealAll, setRevealAll] = useState(false);
  const [revealedIds, setRevealedIds] = useState<ReadonlySet<string>>(
    new Set(),
  );
  const [pasting, setPasting] = useState(false);
  const newLine = useRef<HTMLInputElement>(null);
  // The add line is the list's last: bring it into view, then type.
  const focusNewLine = () => {
    newLine.current?.scrollIntoView({ block: "center", behavior: "smooth" });
    newLine.current?.focus({ preventScroll: true });
  };
  const reveal = revealAll || revealedIds.size > 0;
  const revealed = useRevealedVariables(env, null, reveal);
  const revealedChanges = useRevealedChanges(env, reveal);

  useShortcuts([
    { key: "v", label: "paste .env", run: () => setPasting(true) },
    { key: "n", label: "add variable", run: () => focusNewLine() },
  ]);

  const shared = all.filter((v) => v.scope === "environment");
  const own = service
    ? all.filter((v) => v.scope === "service" && v.service === service)
    : [];
  const ownKeys = new Set(own.map((v) => v.key));
  const byKey = (a: Variable, b: Variable) => a.key.localeCompare(b.key);
  const rows = [...shared.sort(byKey), ...own.sort(byKey)];

  const values = new Map<
    string,
    { live?: string; old?: string; new?: string }
  >();
  for (const v of revealed.data ?? []) values.set(idOf(v), { live: v.value });
  for (const c of revealedChanges.data?.variables ?? []) {
    values.set(idOf(c), { ...values.get(idOf(c)), old: c.old, new: c.new });
  }
  const shown = (v: Variable) => revealAll || revealedIds.has(idOf(v));

  const sharedCount = shared.length;
  const actions = (
    <>
      {isAdmin && (
        <Button onClick={() => setRevealAll(!revealAll)}>
          {revealAll ? "Hide values" : "Reveal values"}
        </Button>
      )}
      <Button onClick={() => setPasting(!pasting)}>
        Paste .env <kbd>V</kbd>
      </Button>
      <Button onClick={focusNewLine}>
        Add variable <kbd>N</kbd>
      </Button>
    </>
  );
  return (
    <div className="flex flex-1 flex-col md:flex-row">
      <nav
        aria-label="Applies to"
        className="flex flex-col gap-0.5 border-b border-line px-2.5 pt-4 pb-3 md:w-[220px] md:flex-none md:border-r md:border-b-0"
      >
        <div className="px-2 pb-1.5 text-2xs text-faint">Applies to</div>
        <Link
          to="/$org/$project/settings/variables/$environment"
          params={{ org, ...env }}
          activeOptions={{ exact: true }}
          className="nav-row"
        >
          <span className="flex-1">Shared · every service</span>
          <span className="text-2xs text-faint">{sharedCount}</span>
        </Link>
        {services.map((s) => {
          const state = serviceState(s);
          const name = s.service.name;
          const ownCount = all.filter(
            (v) => v.scope === "service" && v.service === name,
          ).length;
          return (
            <Link
              key={s.service.id}
              to="/$org/$project/$environment/$service/variables"
              params={{ org, ...env, service: name }}
              activeOptions={{ exact: true }}
              className="nav-row"
            >
              {state.tone === "fail" && state.glyph && (
                <span className="text-fail">
                  <Glyph char={state.glyph} />
                </span>
              )}
              <span className="flex-1 truncate">{name}</span>
              <span className="text-2xs text-faint">
                {ownCount} + {sharedCount} shared
              </span>
            </Link>
          );
        })}
        <p className="px-2 pt-3 text-2xs leading-relaxed text-faint">
          Grey lines come from Shared. A line in a service with the same name
          wins.
        </p>
      </nav>

      <div className="flex w-full max-w-[1400px] min-w-0 flex-1 flex-col gap-4 px-4 pt-6 pb-10 sm:px-7">
        <SectionHead id="vars" right={actions}>
          {service
            ? `${service} sees at runtime`
            : `Shared with every service in ${env.environment}`}
        </SectionHead>

        {pasting && (
          <PasteEnv
            env={env}
            service={service}
            onDone={() => setPasting(false)}
          />
        )}
        <ErrorText error={revealed.error ?? revealedChanges.error} />

        <section
          aria-labelledby="vars"
          className="-mt-2.5 overflow-x-auto rounded-panel border border-line font-mono text-xs"
        >
          <div className="min-w-[640px]">
            {rows.map((v, i) => (
              <Line
                key={idOf(v)}
                n={i + 1}
                env={env}
                variable={v}
                inherited={!!service && v.scope === "environment"}
                overridden={
                  !!service && v.scope === "environment" && ownKeys.has(v.key)
                }
                values={shown(v) ? values.get(idOf(v)) : typedOnly(env, v)}
                all={revealed.data ?? []}
                canReveal={isAdmin && !shown(v)}
                onReveal={() =>
                  setRevealedIds(new Set([...revealedIds, idOf(v)]))
                }
              />
            ))}
            <NewLine
              n={rows.length + 1}
              env={env}
              service={service}
              inputRef={newLine}
            />
          </div>
        </section>
        <p className="-mt-1 text-2xs text-faint">
          <span className="text-ok">+</span> added ·{" "}
          <span className="text-warn">~</span> changed ·{" "}
          <span className="text-fail">−</span> removed ·{" "}
          <span className="font-mono text-info">
            {"$"}
            {"{{ }}"}
          </span>{" "}
          points at another service, → resolved
        </p>

        <ApplyBox
          env={env}
          reveal={revealAll}
          variables={all}
          service={service}
        />
        {footer}
      </div>
    </div>
  );
}

const pendingMark = { add: "+", update: "~", remove: "−" } satisfies Record<
  NonNullable<Variable["pending"]>,
  string
>;

const grid =
  "grid grid-cols-[34px_14px_minmax(120px,200px)_minmax(0,1fr)_auto] items-baseline gap-x-2.5 border-b border-soft py-[7px] pr-3.5";

const pendingRow = {
  add: "bg-add-bg",
  update: "bg-change-bg",
  remove: "bg-remove-bg",
} satisfies Record<NonNullable<Variable["pending"]>, string>;

const pendingText = {
  add: "text-ok",
  update: "text-warn",
  remove: "text-fail",
} satisfies Record<NonNullable<Variable["pending"]>, string>;

function Line({
  n,
  env,
  variable: v,
  inherited,
  overridden,
  values,
  all,
  canReveal,
  onReveal,
}: {
  n: number;
  env: EnvRef;
  variable: Variable;
  /** A shared line on a service's view: grey. */
  inherited: boolean;
  /** A shared line this service replaces with its own. */
  overridden: boolean;
  values: { live?: string; old?: string; new?: string } | undefined;
  /** Every revealed variable, to resolve references. */
  all: Variable[];
  canReveal: boolean;
  onReveal: () => void;
}) {
  const stage = useStageVariables();
  const discard = useDiscardChanges();
  const [editing, setEditing] = useState(false);
  const service = v.scope === "service" ? (v.service ?? null) : null;
  const current = values?.new ?? values?.live;
  const resolved =
    current !== undefined && hasReference(current)
      ? maskUrlPasswords(resolve(current, all))
      : undefined;
  const save = (value: string) =>
    stage.mutate(
      { env, service, set: { [v.key]: value } },
      { onSuccess: () => setEditing(false) },
    );

  return (
    <div
      className={`${grid} group ${v.pending ? pendingRow[v.pending] : "hover:bg-hover"}`}
    >
      <span className="text-right text-faint">{n}</span>
      <span className={v.pending && pendingText[v.pending]}>
        {v.pending && pendingMark[v.pending]}
      </span>
      <span
        className={`truncate ${inherited ? "text-mut" : ""} ${overridden || v.pending === "remove" ? "line-through" : ""}`}
        title={v.key}
      >
        {v.key}
      </span>
      {editing ? (
        <input
          // biome-ignore lint/a11y/noAutofocus: opened by clicking Edit
          autoFocus
          defaultValue={current ?? ""}
          placeholder={current === undefined ? "new value" : undefined}
          aria-label={`${v.key} value`}
          onKeyDown={(e) => {
            if (e.key === "Enter") save(e.currentTarget.value);
            if (e.key === "Escape") setEditing(false);
          }}
          className="min-w-0 rounded-[4px] bg-panel px-1 outline-none ring-1 ring-info"
        />
      ) : (
        <span
          className={`min-w-0 break-all ${inherited ? "text-mut" : ""} ${overridden ? "line-through" : ""} ${current !== undefined && hasReference(current) ? "text-info" : ""}`}
        >
          <Value pending={v.pending} values={values} />
          {canReveal && (
            <button
              type="button"
              className="lnk ml-2 font-sans text-2xs"
              onClick={onReveal}
            >
              Reveal
            </button>
          )}
        </span>
      )}
      <span className="flex items-baseline gap-3 font-sans text-2xs">
        <ErrorText error={stage.error ?? discard.error} />
        {overridden ? (
          <span className="text-faint">overridden</span>
        ) : (
          inherited && !v.pending && <span className="text-faint">shared</span>
        )}
        {v.pending ? (
          <button
            type="button"
            className="lnk"
            disabled={discard.isPending}
            onClick={() =>
              discard.mutate({
                env,
                selection: {
                  variables: [{ key: v.key, ...(service && { service }) }],
                },
              })
            }
            title={`dock discard ${v.key} ${service ? `-s ${service}` : "--shared"} -e ${env.environment}`}
          >
            Discard
          </button>
        ) : (
          !editing && (
            <span className="flex gap-3 opacity-0 group-hover:opacity-100 focus-within:opacity-100">
              <button
                type="button"
                className="lnk"
                title={`dock env set ${v.key}=… ${scopeFlags(env, service)}`}
                onClick={() => setEditing(true)}
              >
                Edit
              </button>
              <button
                type="button"
                className="lnk"
                title={`dock env unset ${v.key} ${scopeFlags(env, service)}`}
                disabled={stage.isPending}
                onClick={() => stage.mutate({ env, service, unset: [v.key] })}
              >
                Remove
              </button>
            </span>
          )
        )}
      </span>
      {resolved !== undefined && resolved !== current && (
        <span className="col-start-4 col-end-6 text-2xs text-faint">
          → {resolved}
        </span>
      )}
    </div>
  );
}

function Value({
  pending,
  values,
}: {
  pending: Variable["pending"];
  values: { live?: string; old?: string; new?: string } | undefined;
}) {
  switch (pending) {
    case "add":
      return <span className="text-fg">{values?.new ?? MASK}</span>;
    case "update":
      return (
        <>
          <span className="text-fg">{values?.new ?? MASK}</span>{" "}
          <span className="text-faint line-through">{values?.old ?? ""}</span>
        </>
      );
    case "remove":
      return <span className="line-through">{values?.old ?? MASK}</span>;
    case undefined:
      return <>{values?.live ?? MASK}</>;
  }
}

/** The last line: type NAME=value and press Enter to stage it. */
function NewLine({
  n,
  env,
  service,
  inputRef,
}: {
  n: number;
  env: EnvRef;
  service: string | null;
  inputRef: RefObject<HTMLInputElement | null>;
}) {
  const stage = useStageVariables();
  const [text, setText] = useState("");
  const parsed = parseDotenv(text);
  const add = () => {
    if (parsed.length === 0) return;
    stage.mutate(
      {
        env,
        service,
        set: Object.fromEntries(parsed.map((p) => [p.name, p.value])),
      },
      { onSuccess: () => setText("") },
    );
  };
  // Focused (Add variable, N), the row is ringed at full contrast and its
  // Enter hint turns white, so the jump to it is plain to see.
  return (
    <div
      className={`${grid} group scroll-my-24 border-b-0 focus-within:bg-panel focus-within:outline-2 focus-within:outline-info`}
    >
      <span className="text-right text-faint">{n}</span>
      <span />
      <input
        ref={inputRef}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Escape") e.currentTarget.blur();
          if (e.key === "Enter") add();
        }}
        placeholder={`NAME=value  ·  for ${service ?? "every service"}`}
        aria-label="New variable, NAME=value"
        title={`dock env set NAME=value ${scopeFlags(env, service)}`}
        className="col-span-2 min-w-0 rounded-[4px] border border-line bg-transparent px-1.5 py-0.5 outline-none placeholder:text-faint focus:border-fg focus:bg-panel"
      />
      <span className="flex items-center gap-1.5 font-sans text-2xs text-faint group-focus-within:text-fg">
        <ErrorText error={stage.error} />
        <button
          type="button"
          className="btn sm"
          disabled={stage.isPending}
          onClick={add}
        >
          Add
        </button>
        ↵
      </span>
    </div>
  );
}

/**
 * Bulk paste of a .env file (or one picked from disk), staged as one
 * change set, or applied at once with everything else staged.
 */
function PasteEnv({
  env,
  service,
  onDone,
}: {
  env: EnvRef;
  service: string | null;
  onDone: () => void;
}) {
  const stage = useStageVariables();
  const [text, setText] = useState("");
  const [file, setFile] = useState(".env");
  const [apply, setApply] = useState(false);
  const parsed = parseDotenv(text);
  return (
    <div className="flex flex-col gap-2">
      <label className="btn sm self-start focus-within:ring-1 focus-within:ring-info">
        Read a .env file
        <input
          type="file"
          className="sr-only"
          onChange={async (e) => {
            const picked = e.target.files?.[0];
            if (!picked) return;
            setFile(picked.name);
            setText(await picked.text());
          }}
        />
      </label>
      <textarea
        // biome-ignore lint/a11y/noAutofocus: opened on request
        autoFocus
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => e.key === "Escape" && onDone()}
        placeholder={"DATABASE_URL=postgres://...\nLOG_LEVEL=info"}
        aria-label=".env contents"
        rows={6}
        className="field w-full resize-y font-mono text-xs"
      />
      <div className="flex flex-wrap items-start gap-2.5">
        <Act
          primary
          cli={`dock env import ${file} ${scopeFlags(env, service)}${apply ? " --deploy" : ""}`}
          disabled={parsed.length === 0 || stage.isPending}
          onClick={() =>
            stage.mutate(
              {
                env,
                service,
                set: Object.fromEntries(parsed.map((v) => [v.name, v.value])),
                apply,
              },
              { onSuccess: onDone },
            )
          }
        >
          {apply ? "Apply" : "Stage"} {plural(parsed.length, "variable")} for{" "}
          {service ?? "the environment"}
        </Act>
        <span className="self-center">
          <Check
            label="apply now, with everything staged"
            checked={apply}
            onChange={setApply}
          />
        </span>
        <Button onClick={onDone}>Cancel</Button>
        <span className="self-center">
          <ErrorText error={stage.error} />
        </span>
      </div>
    </div>
  );
}
