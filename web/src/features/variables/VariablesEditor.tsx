import { Link } from "@tanstack/react-router";
import { type RefObject, useRef, useState } from "react";
import { useDiscardChanges, useStageVariables } from "../../data/mutations";
import {
  useEnvironment,
  useIsAdmin,
  useRevealedChanges,
  useRevealedVariables,
  useVariables,
} from "../../data/queries";
import type { EnvRef, Variable } from "../../data/types";
import { useShortcuts } from "../../shell/keys";
import { plural } from "../../ui/format";
import { Act, Button, ErrorText, Glyph, TitleRow } from "../../ui/kit";
import { serviceState } from "../../ui/status";
import { ApplyBox } from "./ApplyBox";
import { parseDotenv } from "./dotenv";
import { hasReference, maskUrlPasswords, resolve } from "./references";

const MASK = "••••••••••••";

/** Identifies a variable across lists: scope, owning service and key. */
const idOf = (v: { key: string; scope: string; service?: string }) =>
  `${v.scope}:${v.service ?? ""}:${v.key}`;

/**
 * An environment's variables as an .env-like editor: pick what to look at
 * (the environment's shared lines, or what one service sees at runtime),
 * edit lines in place, paste a .env, and apply staged lines together.
 * Values stay masked until an admin reveals them.
 */
export function VariablesEditor({
  env,
  service,
}: {
  env: EnvRef;
  /** The service whose runtime view to show; null for shared lines only. */
  service: string | null;
}) {
  const all = useVariables(env, null);
  const { services } = useEnvironment(env);
  const isAdmin = useIsAdmin();
  const [revealAll, setRevealAll] = useState(false);
  const [revealedIds, setRevealedIds] = useState<ReadonlySet<string>>(
    new Set(),
  );
  const [pasting, setPasting] = useState(false);
  const newLine = useRef<HTMLInputElement>(null);
  const reveal = revealAll || revealedIds.size > 0;
  const revealed = useRevealedVariables(env, null, reveal);
  const revealedChanges = useRevealedChanges(env, reveal);

  useShortcuts([
    { key: "p", label: "paste .env", run: () => setPasting(true) },
    { key: "n", label: "add variable", run: () => newLine.current?.focus() },
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

  return (
    <>
      <TitleRow
        title="Variables"
        context={env.environment}
        actions={
          <>
            {isAdmin && (
              <Button onClick={() => setRevealAll(!revealAll)}>
                {revealAll ? "Hide values" : "Reveal values"}
              </Button>
            )}
            <Button onClick={() => setPasting(!pasting)}>
              Paste .env <kbd>P</kbd>
            </Button>
            <Button onClick={() => newLine.current?.focus()}>
              Add variable <kbd>N</kbd>
            </Button>
          </>
        }
      />

      <div className="flex flex-wrap items-center gap-x-3.5 gap-y-2">
        <span className="text-mut">What</span>
        <div role="tablist" aria-label="Scope" className="seg">
          <Link
            role="tab"
            to="/$project/$environment"
            params={env}
            search={{}}
            aria-selected={service === null}
          >
            environment
          </Link>
          {services.map((s) => {
            const state = serviceState(s);
            return (
              <Link
                key={s.service.id}
                role="tab"
                to="/$project/$environment"
                params={env}
                search={{ service: s.service.name }}
                aria-selected={service === s.service.name}
              >
                {state.tone === "fail" && state.glyph && (
                  <span className="text-fail">
                    <Glyph char={state.glyph} />{" "}
                  </span>
                )}
                {s.service.name}
              </Link>
            );
          })}
        </div>
        <span className="text-mut">
          {service
            ? "sees at runtime. Grey lines come from the environment."
            : `is shared with every service in ${env.environment}.`}
        </span>
      </div>

      {pasting && (
        <PasteEnv
          env={env}
          service={service}
          onDone={() => setPasting(false)}
        />
      )}
      <ErrorText error={revealed.error ?? revealedChanges.error} />

      <section
        aria-label={`${service ?? env.environment} variables`}
        className="overflow-x-auto border border-line py-2 font-mono text-[12.5px]"
      >
        <div className="min-w-[720px]">
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
              values={shown(v) ? values.get(idOf(v)) : undefined}
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
      <p className="-mt-1 text-xs text-mut">
        <span className="text-warn">+</span> added ·{" "}
        <span className="text-warn">~</span> changed ·{" "}
        <span className="text-warn">−</span> removed
        {service &&
          ` · a ${service} line with the same name as an env line overrides it`}{" "}
        · → resolved reference
      </p>

      <ApplyBox env={env} reveal={revealAll} variables={all} />
    </>
  );
}

const pendingMark = { add: "+", update: "~", remove: "−" } satisfies Record<
  NonNullable<Variable["pending"]>,
  string
>;

const grid =
  "grid grid-cols-[34px_16px_70px_minmax(0,1fr)_auto] items-baseline gap-x-2.5 py-1 pr-3.5";

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
      className={`${grid} group ${v.pending ? "bg-stage-bg" : "hover:bg-hover"}`}
    >
      <span className="text-right text-faint">{n}</span>
      <span className="text-warn">{v.pending && pendingMark[v.pending]}</span>
      <span className={inherited ? "text-mut" : ""}>{service ?? "env"}</span>
      {editing ? (
        <span className="flex items-baseline">
          {v.key}=
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
            className="min-w-0 flex-1 bg-transparent outline-none"
          />
        </span>
      ) : (
        <span
          className={`${inherited ? "text-mut" : ""} ${overridden ? "line-through" : ""}`}
        >
          {v.key}=<Value pending={v.pending} values={values} />
          {canReveal && (
            <button
              type="button"
              className="lnk ml-2 font-sans"
              onClick={onReveal}
            >
              Reveal
            </button>
          )}
        </span>
      )}
      <span className="flex gap-3 font-sans">
        <ErrorText error={stage.error ?? discard.error} />
        {overridden && <span className="text-xs text-mut">overridden</span>}
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
          >
            Discard
          </button>
        ) : (
          !editing && (
            <span className="flex gap-3 opacity-0 group-hover:opacity-100 focus-within:opacity-100">
              <button
                type="button"
                className="lnk"
                onClick={() => setEditing(true)}
              >
                Edit
              </button>
              <button
                type="button"
                className="lnk"
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
        <span className="col-start-4 col-end-6 text-xs text-mut">
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
      return <span className="text-white">{values?.new ?? MASK}</span>;
    case "update":
      return (
        <>
          <span className="text-white">{values?.new ?? MASK}</span>{" "}
          <span className="line-through">{values?.old ?? ""}</span>
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
  return (
    <div
      className={`${grid} focus-within:outline-2 focus-within:-outline-offset-2 focus-within:outline-prog`}
    >
      <span className="text-right text-faint">{n}</span>
      <span />
      <span>{service ?? "env"}</span>
      <input
        ref={inputRef}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Escape") e.currentTarget.blur();
          if (e.key !== "Enter" || parsed.length === 0) return;
          stage.mutate(
            {
              env,
              service,
              set: Object.fromEntries(parsed.map((p) => [p.name, p.value])),
            },
            { onSuccess: () => setText("") },
          );
        }}
        placeholder="NAME=value"
        aria-label="New variable, NAME=value"
        className="min-w-0 bg-transparent outline-none"
      />
      <span className="font-sans text-2xs text-mut">
        <ErrorText error={stage.error} /> ↵ add
      </span>
    </div>
  );
}

/** Bulk paste of a .env file, staged as one change set. */
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
  const parsed = parseDotenv(text);
  return (
    <div className="flex flex-col gap-2">
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
          cli={`dock env import .env -e ${env.environment}${service ? ` -s ${service}` : ""}`}
          disabled={parsed.length === 0 || stage.isPending}
          onClick={() =>
            stage.mutate(
              {
                env,
                service,
                set: Object.fromEntries(parsed.map((v) => [v.name, v.value])),
              },
              { onSuccess: onDone },
            )
          }
        >
          Stage {plural(parsed.length, "variable")} for{" "}
          {service ?? "the environment"}
        </Act>
        <Button onClick={onDone}>Cancel</Button>
        <span className="self-center">
          <ErrorText error={stage.error} />
        </span>
      </div>
    </div>
  );
}
