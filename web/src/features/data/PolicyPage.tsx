import { useIsAdmin, useProjectTree } from "../../data/queries";
import { Page } from "../../ui/kit";
import { OrgSettingsBar } from "../org/SettingsBar";
import {
  builtIn,
  fromText,
  type PolicyScope,
  subjectNames,
  usePolicy,
  useSavePolicy,
} from "./dockPolicy";
import { ErrorText } from "./kit";
import { PolicyEditor } from "./PolicyEditor";
import { kindNames, kinds } from "./policy";

/**
 * The rules editor is the data panel's (style G, shared with tempoBase).
 * On the org's settings page its tokens take style A's values, so it reads as
 * part of the app: the app's fonts, a black page, a white primary,
 * the table in a panel.
 */
const styleA = [
  "g",
  "[--g-bg:transparent]",
  "[--g-sans:Inter,system-ui,sans-serif]",
  "[--g-mono:'JetBrains_Mono',ui-monospace,monospace]",
  "[--g-raise:transparent]",
  "[--g-edge:var(--color-edge)]",
  "[--g-line:var(--color-line)]",
  "[--g-grid:var(--color-soft)]",
  "[--g-mut:var(--color-mut)]",
  "[--g-dim:var(--color-sec)]",
  "[--g-accent:var(--color-fg)]",
  "[&_.g-btn]:rounded-ctl",
  "[&_.g-btn]:text-xs",
  "[&_.g-btn.pri]:font-semibold",
  "[&_.g-btn.pri]:text-page",
  "[&_.g-btn.pri:hover:not(:disabled)]:bg-sec",
  "[&_.g-field]:rounded-ctl",
  // The level selects look like the app's selects: its chevron (g.css's
  // background shorthand drops it).
  "[--chevron:url(data:image/svg+xml,%3Csvg%20xmlns=%27http://www.w3.org/2000/svg%27%20width=%2710%27%20height=%2710%27%20viewBox=%270%200%2010%2010%27%20fill=%27none%27%20stroke=%27%23a1a1aa%27%20stroke-width=%271.4%27%3E%3Cpath%20d=%27M2.5%204l2.5%202.5L7.5%204%27/%3E%3C/svg%3E)]",
  "[&_select.g-field]:appearance-none",
  "[&_select.g-field]:pr-[26px]",
  "[&_select.g-field]:bg-(image:--chevron)",
  "[&_select.g-field]:bg-no-repeat",
  "[&_select.g-field]:bg-position-[right_8px_center]",
  "[&_select.g-field:disabled]:bg-none",
  // The rules table in a bordered panel, like the app's other tables.
  "[&_div:has(>.g-table)]:rounded-panel",
  "[&_div:has(>.g-table)]:border",
  "[&_div:has(>.g-table)]:border-line",
  "[&_.g-table_tr:last-child>td]:border-0",
  "[&_.g-table_:is(th,td):first-child]:pl-3.5",
].join(" ");

/** The scope one level up: environment → project → default → built-in. */
const parentOf = (s: PolicyScope): PolicyScope | null =>
  s.environment ? { project: s.project } : s.project ? {} : null;

/**
 * Dock's data access policy page: pick a scope (the default, a project, an
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
  const save = useSavePolicy(scope);
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
    <>
      <OrgSettingsBar />
      <Page>
        <div className="flex flex-wrap items-end gap-3">
          <label className="flex flex-col gap-1.5 text-xs text-mut">
            Policy
            <select
              className="text-sm text-fg"
              value={scope.project ?? ""}
              onChange={(e) =>
                onScope({ project: e.target.value || undefined })
              }
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
            <label className="flex flex-col gap-1.5 text-xs text-mut">
              Environment
              <select
                className="text-sm text-fg"
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
          <span
            className="pb-1.5 font-mono text-2xs text-faint"
            title="The same in the CLI"
          >
            dock db policy show{flags && ` ${flags}`}
          </span>
        </div>

        <div className={styleA}>
          {policy.error ? (
            <ErrorText error={policy.error} />
          ) : !policy.data || (parentScope && !parent.data) ? (
            <p className="text-mut">Loading the policy</p>
          ) : (
            <PolicyEditor
              key={`${scope.project ?? ""}/${scope.environment ?? ""}`}
              policy={policy.data}
              inherited={parentScope ? parent.data : undefined}
              shownKinds={kind ? [kind] : kinds}
              editable={admin}
              save={save}
              subjectNames={subjectNames}
              fromText={(from) => fromText(from, scope)}
              builtIn={builtIn}
              cli={(rules) =>
                [
                  "dock db policy set",
                  ...rules.map(
                    (r) =>
                      `${r.subject.replace("role:", "")}.${r.kind.replace("_", "-")}=${r.level}${r.sensitive === "visible" ? ",visible" : ""}`,
                  ),
                  flags,
                  "--replace",
                ]
                  .filter(Boolean)
                  .join(" ")
              }
            />
          )}
        </div>

        <div className={`${styleA} flex max-w-[860px] flex-col gap-2`}>
          <details className="text-(--g-dim)">
            <summary className="w-fit cursor-pointer">
              How rules resolve
            </summary>
            <div className="mt-2 flex flex-col gap-2 leading-relaxed">
              <p>
                Admins always may see and change the data in database services.
                For everyone else the most specific rule wins, per subject and
                kind of environment: the environment's, then the project's, then
                the default, then the built-in defaults. Rows not set at a scope
                inherit.
              </p>
              <p>
                Tokens follow the tokens rule unless they have their own data
                access (on the Tokens page), and never get more than a user with
                the token's scope, or than their creator has now. On production,
                write acts as propose for tokens: an admin approves the exact
                SQL. Only admins approve changes, set up data access and change
                this policy.
              </p>
            </div>
          </details>
        </div>
      </Page>
    </>
  );
}
