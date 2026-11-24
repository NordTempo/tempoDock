import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type ReactNode, type RefObject, useRef, useState } from "react";
import { useDiscardChanges, useStageVariables } from "../../data/mutations";
import {
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
  MoreItem,
  MoreMenu,
  Page,
  SectionHead,
} from "../../ui/kit";
import { orgQueries } from "../org/data";
import { ApplyBox } from "./ApplyBox";
import { parseDotenv } from "./dotenv";
import { hasReference, maskUrlPasswords, resolve } from "./references";

const MASK = "••••••••••••";

/**
 * A value this session typed (data/typed.ts): the new value of a staged
 * line, so it can be checked before Apply, and the live value once
 * applied, so it can be checked after. The rest stays masked.
 */
function typedOnly(env: EnvRef, v: Variable) {
  const value = typedValue(
    env,
    v.scope === "service" ? v.service : null,
    v.key,
  );
  if (value === undefined || v.pending === "remove") return undefined;
  return v.pending ? { new: value } : { live: value };
}

/** The CLI's flags for a service's variables, or the shared ones. */
const scopeFlags = (env: EnvRef, service: string | null) =>
  `${service ? `-s ${service}` : "--shared"} -e ${env.environment}`;

/** Identifies a variable across lists: scope, owning service and key. */
const idOf = (v: { key: string; scope: string; service?: string }) =>
  `${v.scope}:${v.service ?? ""}:${v.key}`;

/**
 * The values a list shows: what this session revealed (one line, or all
 * of them), with the staged old and new values of revealed lines, and the
 * values this session typed. Shared by both views.
 */
function useShownValues(env: EnvRef, revealAll: boolean) {
  const [revealedIds, setRevealedIds] = useState<ReadonlySet<string>>(
    new Set(),
  );
  const reveal = revealAll || revealedIds.size > 0;
  const revealed = useRevealedVariables(env, null, reveal);
  const revealedChanges = useRevealedChanges(env, reveal);
  const values = new Map<
    string,
    { live?: string; old?: string; new?: string }
  >();
  for (const v of revealed.data ?? []) values.set(idOf(v), { live: v.value });
  for (const c of revealedChanges.data?.variables ?? []) {
    values.set(idOf(c), { ...values.get(idOf(c)), old: c.old, new: c.new });
  }
  const shown = (v: Parameters<typeof idOf>[0]) =>
    revealAll || revealedIds.has(idOf(v));
  return {
    /** Whether a line's values show: in the list, and in ApplyBox. */
    shown,
    valuesOf: (v: Variable) =>
      shown(v) ? values.get(idOf(v)) : typedOnly(env, v),
    show: (v: Variable) => setRevealedIds(new Set([...revealedIds, idOf(v)])),
    all: revealed.data ?? [],
    error: revealed.error ?? revealedChanges.error,
  };
}

/**
 * An environment's variables as an .env-like editor. With a service, its
 * Variables tab: what that service sees, in the order they apply (its
 * own, editable here; the environment's shared ones; the organization's,
 * used by name), each group saying where it is edited. Without, the
 * project Settings' shared lines of one environment, with their apply
 * panel (there is no service header to hold Apply). Lines are edited in
 * place, a .env pastes in, and staged lines go live together with Apply.
 * Values stay masked until an admin shows them. `footer` goes under the
 * shared lines.
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
  if (service) return <ServiceVariables env={env} service={service} />;
  return <SharedVariables env={env} footer={footer} />;
}

/** The project Settings' editor of one environment's shared lines. */
function SharedVariables({ env, footer }: { env: EnvRef; footer?: ReactNode }) {
  const all = useVariables(env, null);
  const isAdmin = useIsAdmin();
  const [revealAll, setRevealAll] = useState(false);
  const [pasting, setPasting] = useState(false);
  const newLine = useRef<HTMLInputElement>(null);
  const values = useShownValues(env, revealAll);
  const shared = all
    .filter((v) => v.scope === "environment")
    .sort((a, b) => a.key.localeCompare(b.key));
  const focusNewLine = useFocusNewLine(newLine);
  useShortcuts([
    { key: "v", label: "paste .env", run: () => setPasting(true) },
    { key: "n", label: "add variable", run: focusNewLine },
  ]);
  return (
    <div className="flex min-w-0 flex-col gap-4">
      <SectionHead
        id="vars"
        aside="every service here sees them, unless it sets the same name"
        right={
          <Actions
            isAdmin={isAdmin}
            revealAll={revealAll}
            onReveal={() => setRevealAll(!revealAll)}
            onPaste={() => setPasting(!pasting)}
            onAdd={focusNewLine}
          />
        }
      >
        Shared with every service in {env.environment}
      </SectionHead>
      {pasting && (
        <PasteEnv env={env} service={null} onDone={() => setPasting(false)} />
      )}
      <ErrorText error={values.error} />
      <Lines labelledBy="vars">
        {shared.map((v, i) => (
          <Line
            key={idOf(v)}
            n={i + 1}
            env={env}
            variable={v}
            values={values.valuesOf(v)}
            all={values.all}
            canReveal={isAdmin && !values.shown(v)}
            onReveal={() => values.show(v)}
          />
        ))}
        <NewLine
          n={shared.length + 1}
          env={env}
          service={null}
          inputRef={newLine}
        />
      </Lines>
      <ApplyBox env={env} shows={values.shown} variables={all} />
      {footer}
    </div>
  );
}

/**
 * A service's Variables tab: what it sees at runtime, in three groups.
 * Its own lines are edited here; the environment's shared lines and the
 * organization's variables are listed with where they are edited. Apply
 * is the service header's.
 */
function ServiceVariables({ env, service }: { env: EnvRef; service: string }) {
  const org = useOrg();
  const all = useVariables(env, null);
  const isAdmin = useIsAdmin();
  const orgVars = useQuery(orgQueries.variables()).data ?? [];
  const [revealAll, setRevealAll] = useState(false);
  const [pasting, setPasting] = useState(false);
  const newLine = useRef<HTMLInputElement>(null);
  const values = useShownValues(env, revealAll);
  const byKey = (a: Variable, b: Variable) => a.key.localeCompare(b.key);
  const own = all
    .filter((v) => v.scope === "service" && v.service === service)
    .sort(byKey);
  const ownKeys = new Set(own.map((v) => v.key));
  const shared = all.filter((v) => v.scope === "environment").sort(byKey);
  const focusNewLine = useFocusNewLine(newLine);
  useShortcuts([
    { key: "v", label: "paste .env", run: () => setPasting(true) },
    { key: "n", label: "add variable", run: focusNewLine },
  ]);
  const usedHere = (v: (typeof orgVars)[number]) =>
    (v.used_by ?? []).some(
      (u) =>
        u.project === env.project &&
        u.environment === env.environment &&
        (!u.service || u.service === service),
    );
  return (
    <Page>
      <section aria-labelledby="own" className="flex flex-col gap-3">
        <SectionHead
          id="own"
          aside={own.length}
          right={
            <Actions
              isAdmin={isAdmin}
              revealAll={revealAll}
              onReveal={() => setRevealAll(!revealAll)}
              onPaste={() => setPasting(!pasting)}
              onAdd={focusNewLine}
            />
          }
        >
          {service}'s own
        </SectionHead>
        {pasting && (
          <PasteEnv
            env={env}
            service={service}
            onDone={() => setPasting(false)}
          />
        )}
        <ErrorText error={values.error} />
        <Lines labelledBy="own">
          {own.map((v, i) => (
            <Line
              key={idOf(v)}
              n={i + 1}
              env={env}
              variable={v}
              values={values.valuesOf(v)}
              all={values.all}
              canReveal={isAdmin && !values.shown(v)}
              onReveal={() => values.show(v)}
            />
          ))}
          <NewLine
            n={own.length + 1}
            env={env}
            service={service}
            inputRef={newLine}
          />
        </Lines>
      </section>

      <section aria-labelledby="shared" className="flex flex-col gap-3">
        <SectionHead
          id="shared"
          aside={shared.length}
          right={
            <Link
              to="/$org/$project/settings"
              params={{ org, project: env.project }}
              hash="variables"
            >
              Edit in {env.project} › Settings
            </Link>
          }
        >
          Shared with every service in {env.environment}
        </SectionHead>
        {shared.length === 0 ? (
          <p className="text-mut">
            None. A shared variable applies to every service here unless the
            service sets the same name.
          </p>
        ) : (
          <Lines labelledBy="shared">
            {shared.map((v, i) => (
              <Line
                key={idOf(v)}
                n={i + 1}
                env={env}
                variable={v}
                readOnly
                overridden={ownKeys.has(v.key) ? service : undefined}
                values={values.valuesOf(v)}
                all={values.all}
                canReveal={isAdmin && !values.shown(v)}
                onReveal={() => values.show(v)}
              />
            ))}
          </Lines>
        )}
      </section>

      {orgVars.length > 0 && (
        <section aria-labelledby="org-vars" className="flex flex-col gap-3">
          <SectionHead
            id="org-vars"
            aside={orgVars.length}
            right={
              <Link to="/$org/settings/variables" params={{ org }}>
                Edit in Settings › Variables
              </Link>
            }
          >
            Organization
          </SectionHead>
          <p className="-mt-1 text-mut">
            Used by name: a value like{" "}
            <span className="font-mono text-info">
              {"${{org."}
              {orgVars[0]?.key}
              {"}}"}
            </span>{" "}
            above reads it.
          </p>
          <Lines labelledBy="org-vars">
            {orgVars.map((v) => (
              <div key={v.key} className={`${grid} hover:bg-hover`}>
                <span />
                <span />
                <span className="truncate" title={v.key}>
                  {v.key}
                </span>
                <span className="font-sans text-mut">
                  {usedHere(v)
                    ? `used in ${env.environment}`
                    : `not used by ${service}`}
                </span>
                <span />
              </div>
            ))}
          </Lines>
        </section>
      )}
    </Page>
  );
}

/** Brings the add line into view, then focuses it (Add variable, N). */
function useFocusNewLine(newLine: RefObject<HTMLInputElement | null>) {
  return () => {
    newLine.current?.scrollIntoView({ block: "center", behavior: "smooth" });
    newLine.current?.focus({ preventScroll: true });
  };
}

/**
 * A list's actions: Add variable (N), and Paste .env (V) and showing every
 * value (admins) in its menu.
 */
function Actions({
  isAdmin,
  revealAll,
  onReveal,
  onPaste,
  onAdd,
}: {
  isAdmin: boolean;
  revealAll: boolean;
  onReveal: () => void;
  onPaste: () => void;
  onAdd: () => void;
}) {
  return (
    <span className="flex items-center gap-2">
      <Button onClick={onAdd}>
        Add variable <kbd>N</kbd>
      </Button>
      <MoreMenu label="More variable actions">
        {(close) => (
          <>
            <MoreItem close={close} keyHint="V" onClick={onPaste}>
              Paste .env
            </MoreItem>
            {isAdmin && (
              <MoreItem close={close} onClick={onReveal}>
                {revealAll ? "Hide values" : "Show every value"}
              </MoreItem>
            )}
          </>
        )}
      </MoreMenu>
    </span>
  );
}

/** The lines of one group, as one .env-like block. */
function Lines({
  labelledBy,
  children,
}: {
  labelledBy: string;
  children: ReactNode;
}) {
  return (
    <fieldset
      aria-labelledby={labelledBy}
      className="m-0 min-w-0 overflow-x-auto rounded-panel border border-line p-0 font-mono text-xs"
    >
      <div className="min-w-[640px]">{children}</div>
    </fieldset>
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

/** What a staged mark means, for its tooltip. */
const pendingWord = {
  add: "added, waits for Apply",
  update: "changed, waits for Apply",
  remove: "removed, waits for Apply",
} satisfies Record<NonNullable<Variable["pending"]>, string>;

const pendingText = {
  add: "text-ok",
  update: "text-warn",
  remove: "text-fail",
} satisfies Record<NonNullable<Variable["pending"]>, string>;

/**
 * One variable: its staged mark (+ added, ~ changed, − removed), name and
 * value, Show while masked, and Edit (Enter stages the new value) with
 * Remove or Discard in its menu. `readOnly` lists a line edited
 * elsewhere (a shared line on a service's tab).
 */
function Line({
  n,
  env,
  variable: v,
  readOnly = false,
  overridden,
  values,
  all,
  canReveal,
  onReveal,
}: {
  n: number;
  env: EnvRef;
  variable: Variable;
  readOnly?: boolean;
  /** The service whose own line of the same name replaces this one. */
  overridden?: string;
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
      <span
        className={v.pending && pendingText[v.pending]}
        title={v.pending && pendingWord[v.pending]}
      >
        {v.pending && pendingMark[v.pending]}
      </span>
      <span
        className={`truncate ${overridden || v.pending === "remove" ? "line-through" : ""}`}
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
          autoComplete="off"
          onKeyDown={(e) => {
            if (e.key === "Enter") save(e.currentTarget.value);
            if (e.key === "Escape") setEditing(false);
          }}
          className="min-w-0 rounded-tag bg-panel px-1 outline-none ring-1 ring-info"
        />
      ) : (
        <span
          className={`min-w-0 break-all ${overridden ? "line-through" : ""} ${current !== undefined && hasReference(current) ? "text-info" : ""}`}
        >
          <Value pending={v.pending} values={values} />
          {canReveal && (
            <button
              type="button"
              className="lnk ml-2 font-sans text-2xs"
              onClick={onReveal}
            >
              Show
            </button>
          )}
        </span>
      )}
      <span className="flex items-baseline gap-3 font-sans text-2xs">
        <ErrorText error={stage.error ?? discard.error} />
        {overridden && (
          <span className="text-mut">{overridden} sets its own</span>
        )}
        {/* A staged add or change is edited like a live line: Enter
            stages the new value in its place. */}
        {!readOnly && v.pending !== "remove" && !editing && (
          <button
            type="button"
            className="lnk"
            title={`dock env set ${v.key}=… ${scopeFlags(env, service)}`}
            onClick={() => setEditing(true)}
          >
            Edit
          </button>
        )}
        {!readOnly && v.pending && (
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
            title={`dock discard ${v.key} ${scopeFlags(env, service)}`}
          >
            Discard
          </button>
        )}
        {!readOnly && !v.pending && (
          <MoreMenu label={`${v.key} actions`}>
            {(close) => (
              <MoreItem
                close={close}
                danger
                cli={`dock env unset ${v.key} ${scopeFlags(env, service)}`}
                disabled={stage.isPending}
                onClick={() => stage.mutate({ env, service, unset: [v.key] })}
              >
                Remove {v.key}
              </MoreItem>
            )}
          </MoreMenu>
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
  // Focused (Add variable, N), the row turns blue, its Enter hint turns
  // white and the line under it says what to type, so the jump shows even
  // though the row was already on screen. No ring: its square corners sat
  // inside the list's rounded ones; the field's own edge marks focus.
  return (
    <div
      className={`${grid} group scroll-my-24 border-b-0 focus-within:bg-info-bg`}
    >
      <span className="text-right text-faint group-focus-within:text-info">
        {n}
      </span>
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
        autoComplete="off"
        title={`dock env set NAME=value ${scopeFlags(env, service)}`}
        className="col-span-2 min-h-[24px] min-w-0 rounded-ctl border border-field bg-transparent px-1.5 py-0.5 outline-none placeholder:text-faint focus:border-info focus:bg-page"
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
      {/* Always there (brighter while the row has focus), so nothing moves
          when focus leaves: a click on Apply right after typing lands. */}
      <span className="col-span-full pb-1 pl-[68px] font-sans text-xs text-faint group-focus-within:text-fg">
        NAME=value, then Enter; several lines add several. Values like {"$"}
        {"{{db.DATABASE_URL}}"} read another service's. They wait for Apply.
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
        autoComplete="off"
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
            label="apply now, with everything waiting to apply"
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
