import { useSuspenseQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, body } from "../../api/client";
import { useAction } from "../../data/mutations";
import { Act, Button, ErrorText } from "../../ui/kit";
import {
  type GrantRequest,
  grantPlace,
  useGroups,
  useRevokeGrant,
  useRoleName,
} from "./data";
import { GrantForm } from "./GrantForm";

// An org agent's access (ADR 0080, phase 6b): one of the organization's
// tokens, whose scope and limit became grants. Its grants with a way to
// end each and give more, and the groups it is in.

const accessKey = (id: number) => ["access", "agent", id];

export function AgentAccess({ id, name }: { id: number; name: string }) {
  const token = String(id);
  const acc = useSuspenseQuery({
    queryKey: accessKey(id),
    queryFn: () =>
      body(
        api.GET("/api/v1/tokens/{token}/access", {
          params: { path: { token } },
        }),
      ),
  }).data;
  const groups = useGroups();
  const roleName = useRoleName();
  const revoke = useRevokeGrant();
  const touched = () => [["access"]];
  const grant = useAction(
    (g: GrantRequest) =>
      body(
        api.POST("/api/v1/tokens/{token}/grants", {
          params: { path: { token } },
          body: g,
        }),
      ),
    touched,
    (g) => `Gave ${name} ${g.role}`,
  );
  const member = useAction(
    ({ group, add }: { group: string; add: boolean }) =>
      body(
        (add ? api.PUT : api.DELETE)("/api/v1/groups/{group}/agents/{token}", {
          params: { path: { group, token } },
        }),
      ),
    touched,
    (v) =>
      v.add ? `Put ${name} in ${v.group}` : `Took ${name} out of ${v.group}`,
  );
  const [adding, setAdding] = useState("");
  const others = groups.filter((g) => !(acc.groups ?? []).includes(g.name));
  return (
    <div className="flex flex-col gap-3 py-2">
      <ul className="flex flex-col">
        {(acc.grants ?? []).map((g) => (
          <li
            key={g.id}
            className="flex items-center justify-between gap-2 border-b border-line py-1.5"
          >
            <span>
              <span title={g.role}>{roleName(g.role)}</span>{" "}
              <span className="text-mut">on {grantPlace(g)}</span>
            </span>
            <Act
              small
              cli={`dock grant revoke ${g.id}`}
              disabled={revoke.isPending}
              onClick={() => revoke.mutate(g.id)}
            >
              End
            </Act>
          </li>
        ))}
      </ul>
      {revoke.error && <ErrorText error={revoke.error} />}
      <GrantForm
        cli={`dock token grant ${name}`}
        pending={grant.isPending}
        error={grant.error}
        onGrant={(g) => grant.mutate(g)}
      />
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-mut">Groups</span>
        {(acc.groups ?? []).map((g) => (
          <Act
            key={g}
            small
            cli={`dock group remove ${g} ${name} --agent`}
            onClick={() => member.mutate({ group: g, add: false })}
          >
            {g} ×
          </Act>
        ))}
        <select
          className="field"
          value={adding}
          onChange={(e) => setAdding(e.target.value)}
        >
          <option value="">Add to a group</option>
          {others.map((g) => (
            <option key={g.id} value={g.name}>
              {g.name}
            </option>
          ))}
        </select>
        <Button
          small
          disabled={!adding || member.isPending}
          onClick={() =>
            member.mutate(
              { group: adding, add: true },
              { onSuccess: () => setAdding("") },
            )
          }
        >
          Add
        </Button>
      </div>
      {member.error && <ErrorText error={member.error} />}
    </div>
  );
}
