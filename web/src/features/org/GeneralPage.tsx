import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { setOrg } from "../../api/client";
import { useIsAdmin, useSession } from "../../data/queries";
import { day } from "../../ui/format";
import { Act, Confirm, ErrorText, Page, Panel, TitleRow } from "../../ui/kit";
import { orgQueries, useDeleteOrg, useLeaveOrg, useRenameOrg } from "./data";

/**
 * The organization itself: its name, address (slug), leaving it, and
 * deleting it while it has no projects.
 */
export function GeneralPage() {
  const org = useSuspenseQuery(orgQueries.org()).data;
  const admin = useIsAdmin();
  const user = useSession()?.kind === "user";
  const rename = useRenameOrg();
  const [name, setName] = useState(org.name);
  return (
    <Page width="max-w-[760px]" gap="gap-5">
      <TitleRow title="General" context={`Your role here: ${org.role}`} />
      <Panel>
        <table>
          <tbody>
            <tr>
              <td className="w-32 py-2.5 text-faint">Name</td>
              <td className="py-1.5">
                {admin ? (
                  <form
                    className="flex flex-wrap items-center gap-2"
                    onSubmit={(e) => {
                      e.preventDefault();
                      rename.mutate(name.trim());
                    }}
                  >
                    <input
                      aria-label="Name"
                      value={name}
                      onChange={(e) => setName(e.target.value)}
                      className="field min-w-0 flex-[1_1_220px] py-[5px]"
                    />
                    <Act
                      small
                      type="submit"
                      cli={`dock org rename ${JSON.stringify(name.trim())}`}
                      disabled={
                        !name.trim() ||
                        name.trim() === org.name ||
                        rename.isPending
                      }
                    >
                      Save
                    </Act>
                    <ErrorText error={rename.error} />
                  </form>
                ) : (
                  org.name
                )}
              </td>
            </tr>
            <tr>
              <td className="py-2.5 text-faint">Address</td>
              <td className="py-2.5 font-mono">
                {window.location.host}/<b className="font-medium">{org.slug}</b>{" "}
                <span className="font-sans text-faint">
                  (the slug can't change yet)
                </span>
              </td>
            </tr>
            <tr>
              <td className="py-2.5 text-faint">Created</td>
              <td className="py-2.5 text-mut">{day(org.created_at)}</td>
            </tr>
          </tbody>
        </table>
      </Panel>
      {user && <Leave name={org.name} />}
      {admin && <Delete name={org.name} slug={org.slug} />}
    </Page>
  );
}

/** Drops everything cached for the org just left, and goes home. */
function useGone() {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  return async () => {
    setOrg(undefined);
    queryClient.removeQueries({ predicate: (q) => q.queryKey[0] !== "orgs" });
    await navigate({ to: "/" });
  };
}

function Leave({ name }: { name: string }) {
  const leave = useLeaveOrg();
  const gone = useGone();
  const [asking, setAsking] = useState(false);
  return (
    <section aria-labelledby="leave" className="flex flex-col gap-2">
      <h2 id="leave" className="text-sm font-semibold">
        Leave {name}
      </h2>
      {asking ? (
        <Confirm
          action={`Leave ${name}`}
          cli="dock org leave"
          pending={leave.isPending}
          error={leave.error}
          onConfirm={() => leave.mutate(undefined, { onSuccess: gone })}
          onCancel={() => setAsking(false)}
        >
          Leave <b className="font-semibold">{name}</b>?{" "}
          <span className="text-mut">
            You lose access to its projects. An admin can invite you again.
          </span>
        </Confirm>
      ) : (
        <p className="flex flex-wrap items-center gap-3 text-mut">
          <Act small cli="dock org leave" onClick={() => setAsking(true)}>
            Leave
          </Act>
          The last admin can't leave; make someone else admin first.
        </p>
      )}
    </section>
  );
}

function Delete({ name, slug }: { name: string; slug: string }) {
  const remove = useDeleteOrg();
  const gone = useGone();
  const [asking, setAsking] = useState(false);
  return (
    <section aria-labelledby="delete" className="flex flex-col gap-2">
      <h2 id="delete" className="text-sm font-semibold">
        Delete {name}
      </h2>
      {asking ? (
        <Confirm
          danger
          action={`Delete ${slug}`}
          cli="dock org delete"
          pending={remove.isPending}
          error={remove.error}
          onConfirm={() => remove.mutate(undefined, { onSuccess: gone })}
          onCancel={() => setAsking(false)}
        >
          Delete <b className="font-semibold">{name}</b>?{" "}
          <span className="text-mut">
            Its members, invites, variables and tokens go. Its audit log stays.
          </span>
        </Confirm>
      ) : (
        <p className="flex flex-wrap items-center gap-3 text-mut">
          <Act
            small
            danger
            cli="dock org delete"
            onClick={() => setAsking(true)}
          >
            Delete organization
          </Act>
          Only an organization without projects (archived ones count).
        </p>
      )}
    </section>
  );
}
