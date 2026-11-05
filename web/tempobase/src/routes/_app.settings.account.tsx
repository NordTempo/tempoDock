import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { Btn, ErrorText, Head } from "../../../src/features/data/kit";
import { useChangePassword, useLogout, useMe } from "../data/queries";
import { minPassword } from "../shell/Entry";

// Your account: who you are, your password, and signing out.
export const Route = createFileRoute("/_app/settings/account")({
  component: Account,
});

const roleNames = { viewer: "Viewer", editor: "Editor", admin: "Admin" };

function Account() {
  const me = useMe();
  const change = useChangePassword();
  const logout = useLogout();
  const navigate = useNavigate();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  return (
    <>
      <Head
        title="Account"
        actions={
          <Btn
            disabled={logout.isPending}
            onClick={() =>
              logout.mutate(undefined, {
                onSuccess: () => navigate({ to: "/login" }),
              })
            }
          >
            Sign out
          </Btn>
        }
      />
      <main className="flex max-w-[420px] flex-col gap-6 px-5 py-6">
        <dl className="grid grid-cols-[64px_minmax(0,1fr)] gap-y-1">
          <dt className="text-(--g-mut)">Name</dt>
          <dd>{me.name}</dd>
          <dt className="text-(--g-mut)">Email</dt>
          <dd>{me.email}</dd>
          <dt className="text-(--g-mut)">Role</dt>
          <dd>{roleNames[me.role]}</dd>
        </dl>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            change.mutate(
              { current_password: current, new_password: next },
              {
                onSuccess: () => {
                  setCurrent("");
                  setNext("");
                },
              },
            );
          }}
        >
          <h2 className="text-[13px] font-semibold">Change password</h2>
          <label className="flex flex-col gap-1.5">
            <span className="text-xs text-(--g-dim)">Current password</span>
            <input
              className="g-field h-8"
              type="password"
              required
              autoComplete="current-password"
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1.5">
            <span className="text-xs text-(--g-dim)">New password</span>
            <input
              className="g-field h-8"
              type="password"
              required
              minLength={minPassword}
              autoComplete="new-password"
              value={next}
              onChange={(e) => setNext(e.target.value)}
            />
            <span className="text-xs text-(--g-mut)">
              At least {minPassword} characters. Your other sessions end.
            </span>
          </label>
          <div className="flex items-center gap-2">
            <Btn primary type="submit" disabled={change.isPending}>
              Change password
            </Btn>
            {change.isSuccess && (
              <span className="text-xs text-(--g-ok)">Changed</span>
            )}
            <ErrorText error={change.error} />
          </div>
        </form>
      </main>
    </>
  );
}
