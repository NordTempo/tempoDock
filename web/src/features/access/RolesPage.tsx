import { useState } from "react";
import { Act, ErrorText, Page } from "../../ui/kit";
import { OrgSettingsBar } from "../org/SettingsBar";
import {
  type AreaRole,
  type Role,
  useCustomRoles,
  useDeleteRole,
  useRoles,
  useSaveRole,
} from "./data";
import { ShadowNote } from "./ShadowNote";

// Settings › Roles (ADR 0080): the built-in area roles, the same in every
// org, and the org's own: built-in roles and single permissions under a
// name ("Release manager"), granted like a built-in one. Selecting one of
// the org's opens its editor beside the list; "new" makes one.

export function RolesPage({
  selected,
  onSelect,
}: {
  selected?: string;
  onSelect: (name: string | undefined) => void;
}) {
  const builtIn = useRoles();
  const own = useCustomRoles();
  const current = own.find((r) => r.name === selected);
  return (
    <>
      <OrgSettingsBar />
      <Page>
        <ShadowNote />
        <div className="flex flex-col gap-6 lg:flex-row">
          <section
            aria-label="Roles"
            className="flex min-w-0 flex-col gap-3 lg:w-[400px]"
          >
            <table>
              <thead>
                <tr>
                  <th>Custom roles</th>
                  <th>Held by</th>
                </tr>
              </thead>
              <tbody>
                {own.map((r) => (
                  <tr
                    key={r.id}
                    className={`cursor-pointer ${r.name === selected ? "bg-sel" : "hover:bg-hover"}`}
                    onClick={() => onSelect(r.name)}
                  >
                    <td className="py-2">
                      <button
                        type="button"
                        className="text-left"
                        onClick={() => onSelect(r.name)}
                      >
                        {r.name}
                      </button>
                    </td>
                    <td className="py-2 text-mut">
                      {[...(r.groups ?? []), ...(r.people ?? [])].join(", ") ||
                        "nobody"}
                    </td>
                  </tr>
                ))}
                {own.length === 0 && (
                  <tr>
                    <td colSpan={2} className="py-2 text-faint">
                      None yet.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
            <div>
              <Act
                cli={`dock role create <name> --include deploy:edit`}
                onClick={() => onSelect("new")}
              >
                New role
              </Act>
            </div>
            <table>
              <thead>
                <tr>
                  <th>Built in</th>
                  <th>Levels</th>
                </tr>
              </thead>
              <tbody>
                {builtIn.map((r) => (
                  <tr key={r.key}>
                    <td className="py-1.5">{r.name}</td>
                    <td className="py-1.5 text-mut">
                      {/* A role with one level has no level names. */}
                      {(r.levels ?? [])
                        .map((l) => l.name)
                        .filter(Boolean)
                        .join(" · ") || "one level"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </section>
          {(current || selected === "new") && (
            <RoleEditor
              key={current?.id ?? "new"}
              role={current}
              builtIn={builtIn}
              onDone={onSelect}
            />
          )}
        </div>
      </Page>
    </>
  );
}

/** The level an area role is included at in a role's includes. */
const levelIn = (r: AreaRole, includes: string[]) =>
  (r.levels ?? []).find((l) => includes.includes(l.role))?.role ?? "";

/** Makes a role, or edits role. */
function RoleEditor({
  role,
  builtIn,
  onDone,
}: {
  role?: Role;
  builtIn: AreaRole[];
  onDone: (name: string | undefined) => void;
}) {
  const save = useSaveRole();
  const remove = useDeleteRole();
  const [name, setName] = useState(role?.name ?? "");
  const [description, setDescription] = useState(role?.description ?? "");
  const [includes, setIncludes] = useState<string[]>(role?.includes ?? []);
  const [perms, setPerms] = useState<string[]>(role?.permissions ?? []);
  const areas = builtIn.filter((r) => r.key !== "full");
  // What the included roles hold, so their permissions show as included.
  const included = new Set(
    areas.flatMap(
      (r) =>
        (r.levels ?? []).find((l) => includes.includes(l.role))?.permissions ??
        [],
    ),
  );
  const holds = new Set([...included, ...perms]);
  const flags = [
    includes.length > 0 && `--include ${includes.join(",")}`,
    perms.length > 0 && `--permission ${perms.join(",")}`,
  ]
    .filter(Boolean)
    .join(" ");
  const cli = role
    ? `dock role edit "${role.name}"${name.trim() !== role.name ? ` --name "${name.trim()}"` : ""} ${flags}`
    : `dock role create "${name.trim() || "<name>"}" ${flags}`;
  return (
    <section
      aria-label={role?.name ?? "New role"}
      className="flex min-w-0 flex-1 flex-col gap-4"
    >
      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate(
            {
              was: role?.name,
              role: {
                name: name.trim(),
                description,
                includes,
                permissions: perms,
              },
            },
            { onSuccess: (r) => onDone(r.name) },
          );
        }}
      >
        <div className="flex flex-wrap gap-2">
          <input
            className="field text-md font-semibold"
            placeholder="Name, like Release manager"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <input
            className="field min-w-0 flex-1"
            placeholder="What it is for"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
          />
        </div>

        <h2 className="font-semibold">Includes</h2>
        <div className="grid grid-cols-[repeat(auto-fill,minmax(220px,1fr))] gap-x-4 gap-y-2">
          {areas.map((r) => (
            <label
              key={r.key}
              className="flex items-center justify-between gap-2"
            >
              <span>{r.name}</span>
              <select
                className="field"
                value={levelIn(r, includes)}
                onChange={(e) => {
                  const keep = includes.filter(
                    (x) => !(r.levels ?? []).some((l) => l.role === x),
                  );
                  setIncludes(
                    e.target.value ? [...keep, e.target.value] : keep,
                  );
                }}
              >
                <option value="">none</option>
                {(r.levels ?? []).map((l) => (
                  <option key={l.role} value={l.role}>
                    {l.name || "yes"}
                  </option>
                ))}
              </select>
            </label>
          ))}
        </div>

        <h2 className="font-semibold">Plus permissions</h2>
        <div className="grid grid-cols-[repeat(auto-fill,minmax(220px,1fr))] gap-x-4 gap-y-1 font-mono text-xs">
          {[
            ...new Set(
              areas.flatMap((r) => r.levels?.at(-1)?.permissions ?? []),
            ),
          ].map((p) => (
            <label key={p} className="flex items-center gap-1.5">
              <input
                type="checkbox"
                className="accent-fg"
                checked={included.has(p) || perms.includes(p)}
                disabled={included.has(p)}
                onChange={(e) =>
                  setPerms(
                    e.target.checked
                      ? [...perms, p]
                      : perms.filter((x) => x !== p),
                  )
                }
              />
              <span className={included.has(p) ? "text-faint" : ""}>{p}</span>
            </label>
          ))}
        </div>
        <p className="text-mut">
          Holds {holds.size} permissions
          {role &&
            `; held by ${[...(role.groups ?? []), ...(role.people ?? [])].join(", ") || "nobody"}, from their next request`}
          .
        </p>
        <div className="flex flex-wrap gap-2">
          <Act
            primary
            type="submit"
            cli={cli.trim()}
            disabled={!name.trim() || holds.size === 0 || save.isPending}
          >
            {role ? "Save role" : "Make role"}
          </Act>
          {role && (
            <Act
              cli={`dock role delete "${role.name}"`}
              disabled={remove.isPending}
              onClick={() =>
                remove.mutate(role.name, { onSuccess: () => onDone(undefined) })
              }
            >
              Remove
            </Act>
          )}
        </div>
        {(save.error || remove.error) && (
          <ErrorText error={save.error ?? remove.error} />
        )}
      </form>
    </section>
  );
}
