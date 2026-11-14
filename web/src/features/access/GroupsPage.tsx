import { useSuspenseQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Act, Button, Confirm, ErrorText, Page } from "../../ui/kit";
import { orgQueries } from "../org/data";
import {
  type Group,
  grantPlace,
  useCreateGroup,
  useDeleteGroup,
  useGrantGroup,
  useGroupMember,
  useGroups,
  useRemoveGroupAgent,
  useRenameGroup,
  useRevokeGrant,
  useRoleName,
} from "./data";
import { GrantForm } from "./GrantForm";
import { ShadowNote } from "./ShadowNote";

// Settings › Groups (ADR 0080): the org's job functions. On the left the
// groups with who is in them and what they give; beside it the selected
// group's editor: its grants (a role at a place) and its members. Every
// action shows its `dock group` command.

export function GroupsPage({
  selected,
  onSelect,
}: {
  selected?: string;
  onSelect: (name: string | undefined) => void;
}) {
  const groups = useGroups();
  const create = useCreateGroup();
  const [name, setName] = useState("");
  const current = groups.find((g) => g.name === selected) ?? groups[0];
  return (
    <Page>
      <ShadowNote />
      <div className="flex flex-col gap-6 lg:flex-row">
        <section
          aria-label="Groups"
          className="flex min-w-0 flex-col gap-3 lg:w-[360px]"
        >
          <table>
            <thead>
              <tr>
                <th>Group</th>
                <th>People</th>
              </tr>
            </thead>
            <tbody>
              {groups.map((g) => (
                <tr
                  key={g.id}
                  className={`cursor-pointer ${g.id === current?.id ? "bg-selected" : "hover:bg-hover"}`}
                  onClick={() => onSelect(g.name)}
                >
                  <td className="py-2">
                    <button
                      type="button"
                      className="text-left"
                      onClick={() => onSelect(g.name)}
                    >
                      {g.name}
                    </button>
                  </td>
                  <td className="py-2 text-mut">{(g.members ?? []).length}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <form
            className="flex gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (name.trim())
                create.mutate(name.trim(), {
                  onSuccess: (g) => {
                    setName("");
                    onSelect(g.name);
                  },
                });
            }}
          >
            <input
              className="field min-w-0 flex-1"
              placeholder="New group, like Contractors"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
            <Act
              type="submit"
              cli={`dock group create ${name.trim() || "<name>"}`}
              disabled={!name.trim() || create.isPending}
            >
              Create
            </Act>
          </form>
          {create.error && <ErrorText error={create.error} />}
        </section>
        {current && (
          <GroupEditor key={current.id} group={current} onRenamed={onSelect} />
        )}
      </div>
    </Page>
  );
}

/** One group: its grants and members, and renaming or removing it. */
function GroupEditor({
  group,
  onRenamed,
}: {
  group: Group;
  onRenamed: (name: string | undefined) => void;
}) {
  const members = useSuspenseQuery(orgQueries.members()).data;
  const grant = useGrantGroup();
  const revoke = useRevokeGrant();
  const member = useGroupMember();
  const agent = useRemoveGroupAgent();
  const rename = useRenameGroup();
  const remove = useDeleteGroup();
  const roleName = useRoleName();
  const [asking, setAsking] = useState<"delete" | null>(null);
  const [newName, setNewName] = useState(group.name);
  const [adding, setAdding] = useState("");
  const others = members.filter(
    (m) => !(group.members ?? []).some((g) => g.user_id === m.id),
  );
  return (
    <section
      aria-label={group.name}
      className="flex min-w-0 flex-1 flex-col gap-4"
    >
      <form
        className="flex flex-wrap items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (newName.trim() && newName.trim() !== group.name)
            rename.mutate(
              { group: group.name, name: newName.trim() },
              { onSuccess: (g) => onRenamed(g.name) },
            );
        }}
      >
        <input
          className="field text-md font-semibold"
          value={newName}
          onChange={(e) => setNewName(e.target.value)}
        />
        <Act
          type="submit"
          cli={`dock group rename "${group.name}" "${newName.trim()}"`}
          disabled={newName.trim() === group.name}
        >
          Rename
        </Act>
        <Act
          cli={`dock group delete "${group.name}"`}
          onClick={() => setAsking("delete")}
        >
          Remove
        </Act>
      </form>
      {(rename.error || remove.error) && (
        <ErrorText error={rename.error ?? remove.error} />
      )}
      {asking === "delete" && (
        <Confirm
          danger
          title={`Remove group ${group.name}?`}
          action="Remove group"
          cli={`dock group delete "${group.name}"`}
          pending={remove.isPending}
          error={remove.error}
          onConfirm={() =>
            remove.mutate(group.name, { onSuccess: () => onRenamed(undefined) })
          }
          onCancel={() => setAsking(null)}
        >
          Its {(group.members ?? []).length} members lose what it gives them.
        </Confirm>
      )}

      <h2 className="font-semibold">Gives</h2>
      {(group.grants ?? []).length === 0 ? (
        <p className="text-faint">Nothing yet.</p>
      ) : (
        <table>
          <thead>
            <tr>
              <th>Role</th>
              <th>On</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {(group.grants ?? []).map((g) => (
              <tr key={g.id}>
                <td className="py-2" title={g.role}>
                  {roleName(g.role)}
                </td>
                <td className="py-2">{grantPlace(g)}</td>
                <td className="py-2 text-right">
                  <Act
                    small
                    cli={`dock group revoke ${g.id}`}
                    disabled={revoke.isPending}
                    onClick={() => revoke.mutate(g.id)}
                  >
                    Remove
                  </Act>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {revoke.error && <ErrorText error={revoke.error} />}
      <GrantForm
        cli={`dock group grant "${group.name}"`}
        pending={grant.isPending}
        error={grant.error}
        onGrant={(g) => grant.mutate({ group: group.name, grant: g })}
      />

      <h2 className="font-semibold">People</h2>
      {(group.members ?? []).length === 0 ? (
        <p className="text-faint">Nobody yet.</p>
      ) : (
        <ul className="flex flex-col">
          {(group.members ?? []).map((m) => (
            <li
              key={m.user_id}
              className="flex items-center justify-between gap-2 border-b border-line py-1.5"
            >
              <span>
                {m.name} <span className="text-mut">{m.email}</span>
              </span>
              <Act
                small
                cli={`dock group remove "${group.name}" ${m.email}`}
                disabled={member.isPending}
                onClick={() =>
                  member.mutate({
                    group: group.name,
                    member: m.email,
                    add: false,
                  })
                }
              >
                Take out
              </Act>
            </li>
          ))}
        </ul>
      )}
      {(group.agents ?? []).length > 0 && (
        <ul className="flex flex-col">
          {(group.agents ?? []).map((ag) => (
            <li
              key={ag.token_id}
              className="flex items-center justify-between gap-2 border-b border-line py-1.5"
            >
              <span>
                {ag.name} <span className="text-mut">token</span>
              </span>
              <Act
                small
                cli={`dock group remove "${group.name}" ${ag.name} --agent`}
                disabled={agent.isPending}
                onClick={() =>
                  agent.mutate({ group: group.name, token: ag.name })
                }
              >
                Take out
              </Act>
            </li>
          ))}
        </ul>
      )}
      <div className="flex flex-wrap items-center gap-2">
        <select
          className="field"
          value={adding}
          onChange={(e) => setAdding(e.target.value)}
        >
          <option value="">Add a member</option>
          {others.map((m) => (
            <option key={m.id} value={m.email}>
              {m.name} ({m.email})
            </option>
          ))}
        </select>
        <Button
          disabled={!adding || member.isPending}
          onClick={() =>
            member.mutate(
              { group: group.name, member: adding, add: true },
              { onSuccess: () => setAdding("") },
            )
          }
        >
          Add
        </Button>
      </div>
      {member.error && <ErrorText error={member.error} />}
    </section>
  );
}
