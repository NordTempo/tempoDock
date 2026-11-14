import { Act, Button, ErrorText } from "../../ui/kit";
import { AccessView } from "./AccessView";
import {
  grantPlace,
  useGrantMember,
  useGrants,
  useRevokeGrant,
  useRoleName,
} from "./data";
import { GrantForm } from "./GrantForm";

// One member on the Members page (ADR 0080): their one-off grants, a
// form for another, and what they can do in all (effective access).

export function PersonAccess({
  email,
  onClose,
}: {
  email: string;
  onClose: () => void;
}) {
  const grants = useGrants().filter((g) => g.user === email);
  const grant = useGrantMember();
  const revoke = useRevokeGrant();
  const roleName = useRoleName();
  return (
    <section aria-label={email} className="flex min-w-0 flex-col gap-3">
      <div className="flex items-center justify-between gap-2">
        <h2 className="font-semibold">{email}</h2>
        <Button small onClick={onClose}>
          Close
        </Button>
      </div>
      <h3 className="font-semibold">One-off grants</h3>
      {grants.length === 0 ? (
        <p className="text-faint">None: their groups decide.</p>
      ) : (
        <ul className="flex flex-col">
          {grants.map((g) => (
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
      )}
      {revoke.error && <ErrorText error={revoke.error} />}
      <GrantForm
        cli={`dock grant ${email}`}
        pending={grant.isPending}
        error={grant.error}
        onGrant={(g) => grant.mutate({ member: email, grant: g })}
      />
      <h3 className="font-semibold">Can</h3>
      <AccessView who={email} />
    </section>
  );
}
