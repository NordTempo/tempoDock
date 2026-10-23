import { useState } from "react";
import type { ServiceRef } from "../../data/types";
import { Act, Button, ErrorText, Panel } from "../../ui/kit";
import {
  type Access,
  type AccessMode,
  accessCli,
  accessText,
  type SetAccess,
  useAccess,
  useSetAccess,
} from "./access";

const modes: { mode: AccessMode; label: string }[] = [
  { mode: "public", label: "Anyone" },
  { mode: "password", label: "Password" },
  { mode: "login", label: "Dock login" },
];

/**
 * Who can open the service's domains (ADR 0030): one line of state, and
 * Change opens the editor in its place.
 */
export function AccessLine({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const access = useAccess(ref);
  const [editing, setEditing] = useState(false);
  if (!access.data) return <ErrorText error={access.error} />;
  if (editing) {
    return (
      <AccessEditor
        serviceRef={ref}
        current={access.data}
        onDone={() => setEditing(false)}
      />
    );
  }
  return (
    <div className="flex flex-wrap items-center gap-x-3.5 gap-y-1">
      <span className="font-medium">{accessText(access.data)}</span>
      <span className="text-faint">{note(access.data.mode, ref)}</span>
      <Button small className="ml-auto" onClick={() => setEditing(true)}>
        Change
      </Button>
    </div>
  );
}

/** What a mode means for visitors, in one line. */
function note(mode: AccessMode, ref: ServiceRef) {
  switch (mode) {
    case "password":
      return "visitors type the username and password; scripts can too";
    case "login":
      return `members who can read ${ref.environment} sign in to Dock; the app gets their email in X-Dock-User`;
    default:
      return "every domain below, generated ones too";
  }
}

/** A note as a sentence of its own: "Visitors type ... too." */
const sentence = (s: string) => `${s.charAt(0).toUpperCase()}${s.slice(1)}.`;

function AccessEditor({
  serviceRef: ref,
  current,
  onDone,
}: {
  serviceRef: ServiceRef;
  current: Access;
  onDone: () => void;
}) {
  const set = useSetAccess(ref);
  const [mode, setMode] = useState(current.mode);
  const [username, setUsername] = useState(current.username ?? "");
  const [password, setPassword] = useState("");
  // A service with a password keeps it when the field is left empty.
  const keeps = current.mode === "password" && password === "";
  const req: SetAccess =
    mode === "password"
      ? { mode, username: username.trim(), ...(password && { password }) }
      : { mode };
  const ready =
    mode !== "password" ||
    (req.username !== "" && (keeps || password.length >= 12));
  return (
    <Panel className="flex flex-col gap-3 px-4 py-3.5">
      <form
        aria-label="Who can open the service's domains"
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          set.mutate(req, { onSuccess: onDone });
        }}
      >
        <div className="flex flex-wrap gap-x-[18px] gap-y-1.5">
          {modes.map((m) => (
            <label key={m.mode} className="flex items-center gap-1.5">
              <input
                type="radio"
                name="access"
                checked={mode === m.mode}
                onChange={() => setMode(m.mode)}
              />
              {m.label}
            </label>
          ))}
        </div>
        {mode !== "public" && (
          <p className="text-mut">{sentence(note(mode, ref))}</p>
        )}
        <div className="flex flex-wrap items-center gap-2.5">
          {mode === "password" && (
            <>
              <input
                aria-label="Username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                placeholder="username"
                maxLength={64}
                autoComplete="off"
                className="field w-[150px] py-[7px] font-mono"
              />
              <input
                aria-label="Password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder={
                  current.mode === "password"
                    ? "empty keeps the current one"
                    : "password, 12+ characters"
                }
                maxLength={72}
                autoComplete="new-password"
                className="field w-[220px] py-[7px]"
              />
            </>
          )}
          <Act
            primary
            type="submit"
            cli={accessCli(ref, req)}
            disabled={!ready || set.isPending}
          >
            {mode === "public" ? "Open to anyone" : "Protect"}
          </Act>
          <Button onClick={onDone}>Cancel</Button>
          <ErrorText error={set.error} />
        </div>
      </form>
    </Panel>
  );
}
