import { useIsAdmin, useProjectTree } from "../../data/queries";
import { ErrorText, Page, TitleRow } from "../../ui/kit";
import {
  builtIn,
  fromText,
  type PolicyScope,
  subjectNames,
  usePolicy,
  useSavePolicy,
} from "./dockPolicy";
import { PolicyEditor, PolicyLegend } from "./PolicyEditor";
import { kindNames, kinds } from "./policy";

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
        />
      )}

      <section className="flex max-w-[860px] flex-col gap-2 text-mut">
        <p>
          The most specific rule wins for each subject and kind of environment:
          the environment's, then the project's, then the default, then the
          built-in defaults. Rows not set at a scope inherit.
        </p>
        <PolicyLegend />
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
