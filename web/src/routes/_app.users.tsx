import { createFileRoute } from "@tanstack/react-router";
import { queries, useUsers } from "../data/queries";
import { TopBar } from "../shell/TopBar";
import { day, since } from "../ui/format";

export const Route = createFileRoute("/_app/users")({
  loader: ({ context }) => context.queryClient.ensureQueryData(queries.users()),
  component: Users,
});

function Users() {
  const users = useUsers();
  return (
    <>
      <TopBar>
        <span className="font-semibold">Users</span>
      </TopBar>
      <div className="px-5 pt-3">
        <table className="tbl">
          <thead>
            <tr>
              <th>Username</th>
              <th>Role</th>
              <th>Created</th>
              <th className="w-full">Last seen</th>
            </tr>
          </thead>
          <tbody>
            {users.map((u) => (
              <tr key={u.id}>
                <td className="font-semibold">{u.username}</td>
                <td>{u.role}</td>
                <td className="text-dim">{day(u.createdAt)}</td>
                <td className="text-dim">
                  {u.lastSeenAt ? since(u.lastSeenAt) : "never"}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}
