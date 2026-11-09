import { useState } from "react";
import { Dialog } from "../../ui/Dialog";
import { Check } from "../../ui/kit";
import { Footer, invalid, useValidation } from "../create/form";
import {
  type CloudflareScope,
  type Redirect,
  useAddRedirect,
  useUpdateRedirect,
} from "./data";
import { scopeFlag } from "./Redirects";

// Adds a redirect, or changes one (ADR 0068): a name in one of the
// level's zones, the URL it goes to, temporary or permanent, and whether
// the request's path and query are kept.

const codes = [
  { code: 302, label: "302 temporary" },
  { code: 301, label: "301 permanent" },
  { code: 307, label: "307" },
  { code: 308, label: "308" },
] as const;

type Code = (typeof codes)[number]["code"];

export default function RedirectDialog({
  scope,
  zones,
  redirect,
  onClose,
}: {
  scope: CloudflareScope;
  /** The level's zones, for the hostname. */
  zones: string[];
  /** The redirect to change; a new one when unset. */
  redirect?: Redirect;
  onClose: () => void;
}) {
  const add = useAddRedirect(scope);
  const update = useUpdateRedirect(scope);
  const [name, setName] = useState("");
  const [zone, setZone] = useState("");
  const [target, setTarget] = useState(redirect?.target ?? "");
  const [code, setCode] = useState<Code>(
    (redirect?.status_code as Code | undefined) ?? 302,
  );
  const [keepPath, setKeepPath] = useState(redirect?.keep_path ?? false);
  const picked = zone || zones[0] || "";
  // A full hostname typed into the name field keeps one zone.
  const sub = name
    .trim()
    .toLowerCase()
    .replace(/\.$/, "")
    .replace(new RegExp(`(^|\\.)${picked.replaceAll(".", "\\.")}$`), "");
  const host = redirect?.hostname ?? (sub ? `${sub}.${picked}` : picked);
  const to = target.trim();
  const v = useValidation();
  const toError = v.required(!to, "Enter the URL to redirect to.");
  const mutation = redirect ? update : add;
  // Where a visit to /a?b goes, with the box as it is.
  const base = to || "https://…";
  const example = keepPath ? `${base.replace(/\/$/, "")}/a?b` : base;
  const flags = `${code === 302 ? "" : ` --code ${code}`}${keepPath ? " --keep-path" : ""}${scopeFlag(scope)}`;
  const cli = redirect
    ? `dock cloudflare redirects set ${host} --to ${to || "<url>"} --code ${code} --keep-path=${keepPath}${scopeFlag(scope)}`
    : `dock cloudflare redirects add ${host || "<hostname>"} ${to || "<url>"}${flags}`;
  return (
    <Dialog
      title={redirect ? `Change ${redirect.hostname}` : "Add redirect"}
      onClose={onClose}
      wide
    >
      <form
        ref={v.form}
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (!v.check(!!to && !!host)) return;
          const done = { onSuccess: onClose };
          if (redirect) {
            update.mutate(
              {
                hostname: redirect.hostname,
                change: { target: to, status_code: code, keep_path: keepPath },
              },
              done,
            );
            return;
          }
          add.mutate(
            {
              hostname: host,
              target: to,
              status_code: code,
              ...(keepPath && { keep_path: true }),
            },
            done,
          );
        }}
      >
        <div className="grid grid-cols-[90px_1fr] items-center gap-x-3 gap-y-3">
          <span className="text-mut">From</span>
          {redirect ? (
            <span className="font-mono">{redirect.hostname}</span>
          ) : (
            <span className="flex flex-wrap items-center gap-2">
              <input
                aria-label="Name"
                data-autofocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="instagram, or empty for the zone"
                className="field min-w-0 flex-[1_1_180px] py-[7px] font-mono"
              />
              <span className="text-mut">.</span>
              <select
                aria-label="Zone"
                value={picked}
                onChange={(e) => setZone(e.target.value)}
                className="field py-[7px] font-mono"
              >
                {zones.map((z) => (
                  <option key={z} value={z}>
                    {z}
                  </option>
                ))}
              </select>
            </span>
          )}
          <span className="text-mut">To</span>
          <span className="flex flex-col gap-1">
            <input
              aria-label="Target URL"
              aria-invalid={!!toError}
              data-autofocus={redirect ? true : undefined}
              value={target}
              onChange={(e) => setTarget(e.target.value)}
              placeholder="https://instagram.com/yours"
              className={`field py-[7px] font-mono ${invalid(toError)}`}
            />
            {toError && (
              <span role="alert" className="text-xs text-fail">
                {toError}
              </span>
            )}
          </span>
          <span className="text-mut">Type</span>
          <span
            role="radiogroup"
            aria-label="Type"
            className="flex flex-wrap gap-x-4 gap-y-1"
          >
            {codes.map((c) => (
              <label
                key={c.code}
                className="inline-flex cursor-pointer items-center gap-1.5"
              >
                <input
                  type="radio"
                  name="code"
                  className="m-[4px] size-[16px] accent-fg"
                  checked={code === c.code}
                  onChange={() => setCode(c.code)}
                />
                {c.label}
              </label>
            ))}
          </span>
          <span className="text-mut">Path</span>
          <span className="flex flex-wrap items-center gap-x-2">
            <Check
              label="Keep path and query"
              checked={keepPath}
              onChange={setKeepPath}
            />
            <span className="font-mono text-xs text-faint">
              /a?b → {example}
            </span>
          </span>
        </div>
        <p className="text-mut">
          {code === 301 || code === 308
            ? "Browsers keep a permanent redirect, so a later change may not reach people who followed it."
            : "Cloudflare redirects at its edge; no server is involved."}
        </p>
        <Footer
          className="mt-1"
          submit={
            mutation.isPending
              ? "Saving…"
              : redirect
                ? "Save"
                : `Add ${host || "redirect"}`
          }
          pending={mutation.isPending}
          problem={v.problem([!to && "a URL"])}
          error={mutation.error}
          cli={[cli]}
          onClose={onClose}
        />
      </form>
    </Dialog>
  );
}
