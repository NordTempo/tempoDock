import { useState } from "react";
import { useIsAdmin, useProjectTree } from "../../data/queries";
import { Button, ErrorText, Page, Scroll, TitleRow } from "../../ui/kit";
import {
  fromText,
  kindNames,
  kinds,
  levelNotes,
  levels,
  type PolicyScope,
  sensitivities,
  subjectNames,
  subjects,
  usePolicy,
  useSavePolicy,
} from "./policy";
import type {
  DataEnvironmentKind,
  DataLevel,
  DataPolicy,
  DataPolicyRule,
  DataSensitivity,
  DataSubject,
} from "./types";

/** A rule's setting at one scope; null inherits. */
type Setting = { level: DataLevel; sensitive: DataSensitivity } | null;
type Draft = Record<string, Setting>;

const cell = (subject: DataSubject, kind: DataEnvironmentKind) =>
  `${subject} ${kind}`;

// The built-in defaults (ADR 0016 section 4), what the default policy
// inherits. Only shown for a rule an admin is about to clear.
const builtIn = (
  subject: DataSubject,
  kind: DataEnvironmentKind,
): NonNullable<Setting> => ({
  level:
    subject === "role:deploy" && kind === "non_production" ? "read" : "none",
  sensitive: "masked",
});

/** The scope one level up: environment → project → default → built-in. */
const parentOf = (s: PolicyScope): PolicyScope | null =>
  s.environment ? { project: s.project } : s.project ? {} : null;

/**
 * The data access policy page: pick a scope (the default, a project, an
 * environment), see each subject's effective access per environment kind
 * and where it comes from, and (admins) set or clear rules at that scope.
 */
export function PolicyPage({
  scope,
  onScope,
}: {
  scope: PolicyScope;
  onScope: (next: PolicyScope) => void;
}) {
  const admin = useIsAdmin();
  const tree = useProjectTree();
  const policy = usePolicy(scope);
  const parentScope = parentOf(scope);
  const parent = usePolicy(parentScope ?? {});
  const project = tree.find((p) => p.project.name === scope.project);
  const kind = project?.environments.find(
    (e) => e.environment.name === scope.environment,
  )?.environment.kind;
  const flags = [
    scope.project && `-p ${scope.project}`,
    scope.environment && `-e ${scope.environment}`,
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <Page>
      <TitleRow
        title="Data access"
        context="Who may see and change the data in database services. Admins always may."
      />
      <div className="flex flex-wrap items-end gap-3">
        <label className="flex flex-col gap-1.5">
          <span>Policy</span>
          <select
            className="field py-[7px]"
            value={scope.project ?? ""}
            onChange={(e) => onScope({ project: e.target.value || undefined })}
          >
            <option value="">default, every project</option>
            {tree.map((p) => (
              <option key={p.project.id} value={p.project.name}>
                project {p.project.name}
              </option>
            ))}
          </select>
        </label>
        {project && (
          <label className="flex flex-col gap-1.5">
            <span>Environment</span>
            <select
              className="field py-[7px]"
              value={scope.environment ?? ""}
              onChange={(e) =>
                onScope({
                  project: scope.project,
                  environment: e.target.value || undefined,
                })
              }
            >
              <option value="">every environment</option>
              {project.environments.map(({ environment: e }) => (
                <option key={e.id} value={e.name}>
                  {e.name} ({kindNames[e.kind]})
                </option>
              ))}
            </select>
          </label>
        )}
        <span className="pb-2 font-mono text-2xs text-mut">
          dock db policy show {flags}
        </span>
      </div>

      {policy.error ? (
        <ErrorText error={policy.error} />
      ) : !policy.data || (parentScope && !parent.data) ? (
        <p className="text-mut">Loading the policy</p>
      ) : (
        <PolicyTable
          key={`${scope.project ?? ""}/${scope.environment ?? ""}`}
          scope={scope}
          policy={policy.data}
          inherited={parentScope ? parent.data : undefined}
          shownKinds={kind ? [kind] : kinds}
          editable={admin}
        />
      )}

      <section className="flex max-w-[860px] flex-col gap-2 text-mut">
        <p>
          The most specific rule wins for each subject and kind of environment:
          the environment's, then the project's, then the default, then the
          built-in defaults. Rows not set at a scope inherit.
        </p>
        <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-0.5">
          {levels.map((l) => (
            <div key={l} className="contents">
              <dt className="font-mono text-white">{l}</dt>
              <dd>{levelNotes[l]}</dd>
            </div>
          ))}
          <dt className="font-mono text-white">masked</dt>
          <dd>sensitive columns are never shown</dd>
          <dt className="font-mono text-white">visible</dt>
          <dd>sensitive columns show after an audited reveal</dd>
        </dl>
        <p>
          Tokens follow the tokens rule unless they have their own data access
          (on the Tokens page), and never get more than a user with the token's
          scope, or than their creator has now. On production, write acts as
          propose for tokens: an admin approves the exact SQL. Only admins
          approve changes, set up data access and change this policy.
        </p>
      </section>
    </Page>
  );
}

/** Each subject × kind: the rule set here, the effective result and why. */
function PolicyTable({
  scope,
  policy,
  inherited,
  shownKinds,
  editable,
}: {
  scope: PolicyScope;
  policy: DataPolicy;
  /** The parent scope's policy; its effective rules are what this inherits. */
  inherited?: DataPolicy;
  shownKinds: readonly DataEnvironmentKind[];
  editable: boolean;
}) {
  const initial: Draft = {};
  for (const r of policy.rules ?? []) {
    initial[cell(r.subject, r.kind)] = {
      level: r.level,
      sensitive: r.sensitive,
    };
  }
  const [draft, setDraft] = useState(initial);
  const save = useSavePolicy(scope);
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
    const find = (rules: DataPolicyRule[] | null | undefined) =>
      (rules ?? []).find((r) => r.subject === subject && r.kind === kind);
    // Not set here and not changed: the server's answer.
    const current = find(policy.effective);
    if (current && current.from !== policy.scope) {
      return { ...current, why: `from ${fromText(current.from, scope)}` };
    }
    const up = find(inherited?.effective);
    if (up) return { ...up, why: `from ${fromText(up.from, scope)}` };
    return { ...builtIn(subject, kind), why: "from the built-in defaults" };
  };

  const rules: DataPolicyRule[] = Object.entries(draft).flatMap(
    ([key, value]) => {
      const [subject, kind] = key.split(" ");
      const s = subjects.find((x) => x === subject);
      const k = shownKinds.find((x) => x === kind);
      return value && s && k ? [{ subject: s, kind: k, ...value }] : [];
    },
  );

  return (
    <section aria-label="Rules" className="flex flex-col gap-3">
      <Scroll>
        <table>
          <thead>
            <tr>
              <th>Subject</th>
              <th>Environments</th>
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
                    <td className="py-2 pr-4">{subjectNames[subject]}</td>
                    <td className="py-2 pr-4">{kindNames[kind]}</td>
                    <td className="py-1.5 pr-4">
                      <span className="flex gap-2">
                        <select
                          aria-label={`${subjectNames[subject]}, ${kindNames[kind]}: level`}
                          className="field py-1"
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
                            className="field py-1"
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
                    <td className="py-2 pr-4 font-mono">
                      <span
                        className={
                          effective.level === "none" ? "text-mut" : "text-white"
                        }
                      >
                        {effective.level}
                      </span>
                      <span className="text-mut"> · {effective.sensitive}</span>
                    </td>
                    <td className="w-full py-2 text-mut">
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
          <Button
            primary
            disabled={!dirty || save.isPending}
            onClick={() => save.mutate(rules)}
          >
            Save rules
          </Button>
          <Button disabled={!dirty} onClick={() => setDraft(initial)}>
            Discard
          </Button>
          <ErrorText error={save.error} />
        </div>
      )}
    </section>
  );
}
