import { useState } from "react";
import { useProjects, useProjectTree } from "../../data/queries";
import { Act, ErrorText } from "../../ui/kit";
import {
  type GrantRequest,
  grantFlags,
  useCustomRoles,
  useRoles,
} from "./data";

// A role at a place (ADR 0080): a built-in role, the org or a project or
// one environment, optionally some environment kinds or a while. The
// group editor and a member's one-off grants both use it.

const kindsList = ["production", "other", "previews"] as const;
type Kind = (typeof kindsList)[number];
const lifetimes: [number, string][] = [
  [0, "no end"],
  [4, "4 hours"],
  [24, "1 day"],
  [168, "7 days"],
  [720, "30 days"],
];

export function GrantForm({
  cli,
  pending,
  error,
  onGrant,
}: {
  /** The CLI command before the role and flags. */
  cli: string;
  pending: boolean;
  error: Error | null;
  onGrant: (g: GrantRequest) => void;
}) {
  const roles = useRoles();
  const own = useCustomRoles();
  const projects = useProjects();
  const tree = useProjectTree();
  const [role, setRole] = useState("");
  const [project, setProject] = useState("");
  const [environment, setEnvironment] = useState("");
  const [kinds, setKinds] = useState<Kind[]>([]);
  const [hours, setHours] = useState(0);
  const envs = tree.find((p) => p.project.name === project)?.environments ?? [];
  const grant: GrantRequest = {
    role,
    project: project || undefined,
    environment: environment || undefined,
    kinds:
      environment || kinds.length === 3
        ? undefined
        : kinds.length
          ? kinds
          : undefined,
    for_hours: hours || undefined,
  };
  return (
    <form
      className="flex flex-wrap items-end gap-2.5"
      onSubmit={(e) => {
        e.preventDefault();
        if (role) onGrant(grant);
      }}
    >
      <label className="flex flex-col gap-1.5">
        <span className="text-xs text-faint">Role</span>
        <select
          className="field"
          value={role}
          onChange={(e) => setRole(e.target.value)}
        >
          <option value="">Pick a role</option>
          {own.length > 0 && (
            <optgroup label="The organization's">
              {own.map((r) => (
                <option
                  key={r.id}
                  value={r.name}
                  title={(r.holds ?? []).join(" ")}
                >
                  {r.name}
                </option>
              ))}
            </optgroup>
          )}
          {roles.map((r) => (
            <optgroup key={r.key} label={r.name}>
              {(r.levels ?? []).map((l) => (
                <option
                  key={l.role}
                  value={l.role}
                  title={(l.permissions ?? []).join(" ")}
                >
                  {r.name}
                  {l.name ? ` ${l.name}` : ""}
                </option>
              ))}
            </optgroup>
          ))}
        </select>
      </label>
      <label className="flex flex-col gap-1.5">
        <span className="text-xs text-faint">On</span>
        <select
          className="field"
          value={project}
          onChange={(e) => {
            setProject(e.target.value);
            setEnvironment("");
          }}
        >
          <option value="">the organization</option>
          {projects.map((p) => (
            <option key={p.name} value={p.name}>
              {p.name}
            </option>
          ))}
        </select>
      </label>
      {project && (
        <label className="flex flex-col gap-1.5">
          <span className="text-xs text-faint">Environment</span>
          <select
            className="field"
            value={environment}
            onChange={(e) => setEnvironment(e.target.value)}
          >
            <option value="">every one</option>
            {envs.map(({ environment: e }) => (
              <option key={e.name} value={e.name}>
                {e.name}
              </option>
            ))}
          </select>
        </label>
      )}
      {!environment && (
        <fieldset>
          <legend className="mb-1.5 text-xs text-faint">
            Kinds (none: all)
          </legend>
          <span className="flex min-h-[34px] items-center gap-2.5">
            {kindsList.map((k) => (
              <label key={k} className="flex items-center gap-1">
                <input
                  type="checkbox"
                  className="accent-fg"
                  checked={kinds.includes(k)}
                  onChange={(e) =>
                    setKinds(
                      e.target.checked
                        ? [...kinds, k]
                        : kinds.filter((x) => x !== k),
                    )
                  }
                />
                {k}
              </label>
            ))}
          </span>
        </fieldset>
      )}
      <label className="flex flex-col gap-1.5">
        <span className="text-xs text-faint">Ends</span>
        <select
          className="field"
          value={hours}
          onChange={(e) => setHours(Number(e.target.value))}
        >
          {lifetimes.map(([h, label]) => (
            <option key={h} value={h}>
              {label}
            </option>
          ))}
        </select>
      </label>
      <Act
        primary
        type="submit"
        disabled={!role || pending}
        cli={`${cli} ${role.includes(" ") ? `"${role}"` : role || "<role>"} ${grantFlags(grant)}`.trim()}
      >
        Grant
      </Act>
      {error && <ErrorText error={error} />}
    </form>
  );
}
