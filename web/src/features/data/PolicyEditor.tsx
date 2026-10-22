import { useState } from "react";
import { Btn, ErrorText, Scroll } from "./kit";
import {
  kindNames,
  levelNotes,
  levels,
  type PolicyRule,
  sensitivities,
  subjects,
} from "./policy";
import type {
  DataEnvironmentKind,
  DataLevel,
  DataSensitivity,
  DataSubject,
} from "./types";

// One scope's data access rules (ADR 0016 section 4), shared by Dock's
// policy page (default, project, environment) and tempoBase's (default,
// connection). Style G: render inside a `.g` element.

/**
 * A policy at one scope: its rules and the effective ones, each saying
 * which scope (`F`, the host's scope names) it comes from.
 */
export type ScopedPolicy<F extends string> = {
  scope: F;
  rules: readonly PolicyRule[] | null;
  effective: readonly (PolicyRule & { readonly from?: F })[] | null;
};

/** A rule's setting at one scope; null inherits. */
type Setting = { level: DataLevel; sensitive: DataSensitivity } | null;
type Draft = Record<string, Setting>;

const cell = (subject: DataSubject, kind: DataEnvironmentKind) =>
  `${subject} ${kind}`;

/**
 * Each subject × kind: the rule set here, the effective result and why,
 * and (when editable) Save and Discard. Mount with a key per scope so the
 * draft starts over.
 */
export function PolicyEditor<F extends string>({
  policy,
  inherited,
  shownKinds,
  kindColumn = "Environments",
  editable,
  save,
  subjectNames,
  fromText,
  builtIn,
}: {
  policy: ScopedPolicy<F>;
  /** The parent scope's policy; its effective rules are what this inherits. */
  inherited?: ScopedPolicy<F>;
  shownKinds: readonly DataEnvironmentKind[];
  /** The kind column's heading: what is production or not. */
  kindColumn?: string;
  editable: boolean;
  /** Replaces the rules set at this scope. */
  save: {
    mutate: (rules: PolicyRule[]) => void;
    isPending: boolean;
    error: Error | null;
  };
  /** The subjects in the host's words ("read users", "viewers"). */
  subjectNames: Record<DataSubject, string>;
  /** Where an effective rule comes from, as a phrase after "from". */
  fromText: (from: F | undefined) => string;
  /** The host's built-in defaults: what the top scope inherits. */
  builtIn: (
    subject: DataSubject,
    kind: DataEnvironmentKind,
  ) => Pick<PolicyRule, "level" | "sensitive">;
}) {
  const initial: Draft = {};
  for (const r of policy.rules ?? []) {
    initial[cell(r.subject, r.kind)] = {
      level: r.level,
      sensitive: r.sensitive,
    };
  }
  const [draft, setDraft] = useState(initial);
  const dirty = JSON.stringify(draft) !== JSON.stringify(initial);
  const set = (key: string, value: Setting) =>
    setDraft((d) => {
      const { [key]: _, ...rest } = d;
      return value ? { ...rest, [key]: value } : rest;
    });

  /** What a row resolves to with the draft: set here, or inherited. */
  const resolve = (subject: DataSubject, kind: DataEnvironmentKind) => {
    const here = draft[cell(subject, kind)];
    if (here) return { ...here, why: "set here" };
    const find = (rules: ScopedPolicy<F>["effective"] | undefined) =>
      (rules ?? []).find((r) => r.subject === subject && r.kind === kind);
    // Not set here and not changed: the server's answer.
    const current = find(policy.effective);
    if (current && current.from !== policy.scope) {
      return { ...current, why: `from ${fromText(current.from)}` };
    }
    const up = find(inherited?.effective);
    if (up) return { ...up, why: `from ${fromText(up.from)}` };
    return { ...builtIn(subject, kind), why: "from the built-in defaults" };
  };

  const rules: PolicyRule[] = Object.entries(draft).flatMap(([key, value]) => {
    const [subject, kind] = key.split(" ");
    const s = subjects.find((x) => x === subject);
    const k = shownKinds.find((x) => x === kind);
    return value && s && k ? [{ subject: s, kind: k, ...value }] : [];
  });

  return (
    <section aria-label="Rules" className="flex flex-col gap-3">
      <Scroll>
        <table className="g-table">
          <thead>
            <tr>
              <th>Subject</th>
              <th>{kindColumn}</th>
              <th>Set here</th>
              <th>Effective</th>
              <th>Why</th>
            </tr>
          </thead>
          <tbody>
            {subjects.flatMap((subject) =>
              shownKinds.map((kind) => {
                const key = cell(subject, kind);
                const here = draft[key] ?? null;
                const effective = resolve(subject, kind);
                const tokenNote =
                  subject === "tokens" &&
                  kind === "production" &&
                  effective.level === "write"
                    ? "; write acts as propose for tokens"
                    : "";
                return (
                  <tr key={key}>
                    <td className="font-medium">{subjectNames[subject]}</td>
                    <td className="text-(--g-dim)">{kindNames[kind]}</td>
                    <td>
                      <span className="flex gap-2">
                        <select
                          aria-label={`${subjectNames[subject]}, ${kindNames[kind]}: level`}
                          className="g-field h-7"
                          disabled={!editable}
                          value={here?.level ?? ""}
                          onChange={(e) => {
                            const level = levels.find(
                              (l) => l === e.target.value,
                            );
                            set(
                              key,
                              level
                                ? {
                                    level,
                                    sensitive: here?.sensitive ?? "masked",
                                  }
                                : null,
                            );
                          }}
                        >
                          <option value="">inherit</option>
                          {levels.map((l) => (
                            <option key={l}>{l}</option>
                          ))}
                        </select>
                        {here && (
                          <select
                            aria-label={`${subjectNames[subject]}, ${kindNames[kind]}: sensitive columns`}
                            className="g-field h-7"
                            disabled={!editable}
                            value={here.sensitive}
                            onChange={(e) => {
                              const sensitive = sensitivities.find(
                                (s) => s === e.target.value,
                              );
                              if (sensitive) set(key, { ...here, sensitive });
                            }}
                          >
                            {sensitivities.map((s) => (
                              <option key={s}>{s}</option>
                            ))}
                          </select>
                        )}
                      </span>
                    </td>
                    <td>
                      <span
                        className={
                          effective.level === "none" ? "text-(--g-mut)" : ""
                        }
                      >
                        {effective.level}
                      </span>
                      <span className="text-(--g-mut)">
                        {" "}
                        · {effective.sensitive}
                      </span>
                    </td>
                    <td className="w-full text-(--g-mut)">
                      {effective.why}
                      {tokenNote}
                    </td>
                  </tr>
                );
              }),
            )}
          </tbody>
        </table>
      </Scroll>
      {editable && (
        <div className="flex flex-wrap items-center gap-2.5">
          <Btn
            primary
            disabled={!dirty || save.isPending}
            onClick={() => save.mutate(rules)}
          >
            Save rules
          </Btn>
          <Btn disabled={!dirty} onClick={() => setDraft(initial)}>
            Discard
          </Btn>
          <ErrorText error={save.error} />
        </div>
      )}
    </section>
  );
}

/** What the levels and sensitive settings mean. */
export function PolicyLegend() {
  return (
    <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-0.5 text-(--g-mut)">
      {levels.map((l) => (
        <div key={l} className="contents">
          <dt className="font-mono text-(--g-text)">{l}</dt>
          <dd>{levelNotes[l]}</dd>
        </div>
      ))}
      <dt className="font-mono text-(--g-text)">masked</dt>
      <dd>sensitive columns are never shown</dd>
      <dt className="font-mono text-(--g-text)">visible</dt>
      <dd>sensitive columns show after an audited reveal</dd>
    </dl>
  );
}
