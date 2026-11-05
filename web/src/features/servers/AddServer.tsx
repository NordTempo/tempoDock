import { useState } from "react";
import { useAddServer } from "../../data/mutations";
import { useServers } from "../../data/queries";
import type { AddedServer, NewServer, ServerStatus } from "../../data/types";
import { useKeydown } from "../../shell/keys";
import { until } from "../../ui/format";
import { Button, Check, CopyButton, ErrorText, StateText } from "../../ui/kit";
import { serverState } from "../../ui/status";
import { useValidation } from "../create/form";

const blank: NewServer = {
  name: "",
  accepts_production: false,
  accepts_non_production: true,
  builder: false,
};

/** `dock server add` with the form's flags. */
const cliOf = (f: NewServer) =>
  [
    "dock server add",
    f.name || "<name>",
    f.accepts_production && "--production",
    f.accepts_non_production && "--non-production",
    f.builder && "--builder",
  ]
    .filter(Boolean)
    .join(" ");

/**
 * Admin form to add a server: name, the kinds it accepts and whether it
 * builds images. Once added it shows the install command and join token
 * (once), then follows the new server from joining to online.
 */
export function AddServer({ onClose }: { onClose: () => void }) {
  const add = useAddServer();
  const [form, setForm] = useState(blank);
  const set = (patch: Partial<NewServer>) =>
    setForm((f) => ({ ...f, ...patch }));
  useKeydown((event) => {
    if (event.key === "Escape" && !add.data) onClose();
  });

  const v = useValidation();
  const nameOk = /^[a-z0-9][a-z0-9-]*$/.test(form.name);
  const nameError =
    v.required(!form.name, "Name the server, like homelab.") ??
    v.required(
      !!form.name && !nameOk,
      "Use lowercase letters, digits and dashes.",
    );

  if (add.data) return <Added added={add.data} onDone={onClose} />;
  return (
    <form
      ref={v.form}
      noValidate
      aria-label="Add a server"
      className="flex flex-col gap-2 border-y border-line py-3"
      onSubmit={(e) => {
        e.preventDefault();
        if (v.check(nameOk)) add.mutate(form);
      }}
    >
      <div className="flex flex-wrap items-center gap-x-5 gap-y-2">
        <label className="flex items-center gap-2">
          <span className="text-mut">Name</span>
          <input
            // biome-ignore lint/a11y/noAutofocus: the form opens to be typed into
            autoFocus
            required
            pattern="[a-z0-9][a-z0-9\-]*"
            title="Lower case letters, digits and dashes"
            placeholder="homelab"
            spellCheck={false}
            value={form.name}
            onChange={(e) => set({ name: e.target.value.trim() })}
            aria-invalid={!!nameError}
            className={`field w-44 font-mono ${nameError ? "border-fail" : ""}`}
          />
        </label>
        <span className="inline-flex flex-wrap items-center gap-x-3">
          <span className="text-mut">Accepts</span>
          <Check
            label="production"
            checked={form.accepts_production}
            onChange={(on) => set({ accepts_production: on })}
          />
          <Check
            label="non-production"
            checked={form.accepts_non_production}
            onChange={(on) => set({ accepts_non_production: on })}
          />
        </span>
        <Check
          label="builds images"
          checked={form.builder}
          onChange={(on) => set({ builder: on })}
        />
        <span className="flex gap-2">
          <Button primary small type="submit" disabled={add.isPending}>
            Add server
          </Button>
          <Button small onClick={onClose}>
            Cancel
          </Button>
        </span>
      </div>
      {nameError && (
        <span role="alert" className="text-xs text-fail">
          {nameError}
        </span>
      )}
      <code className="font-mono text-2xs text-mut">{cliOf(form)}</code>
      <ErrorText error={add.error} />
    </form>
  );
}

/** A secret on one line with its copy button. */
function Secret({ label, text }: { label: string; text: string }) {
  return (
    <div className="flex flex-wrap items-center gap-2.5 rounded-ctl border border-line bg-side px-3 py-2">
      <span className="w-28 shrink-0 text-xs whitespace-nowrap text-mut">
        {label}
      </span>
      <code className="min-w-0 flex-1 overflow-x-auto font-mono whitespace-nowrap">
        {text}
      </code>
      <CopyButton text={text} />
    </div>
  );
}

const waiting: Record<ServerStatus, string> = {
  joining: "Waiting for it to connect. This updates by itself.",
  online: "Connected and ready for environments.",
  offline: "It connected once and is offline now.",
  incompatible:
    "It connected, but runs a dock version this control plane can't use. Upgrade it.",
  revoked: "Its key was revoked before it was used.",
};

/** The install command and join token, shown once, and the live status. */
function Added({ added, onDone }: { added: AddedServer; onDone: () => void }) {
  const { name } = added.server;
  // Status events patch the list (data/live.ts).
  const status =
    useServers().find((s) => s.name === name)?.status ?? added.server.status;
  return (
    <section
      aria-labelledby="added-server"
      className="flex flex-col gap-2.5 rounded-panel border border-ok-edge bg-ok-bg/30 p-4"
    >
      <h2 id="added-server" className="text-[15px] font-semibold">
        <span className="text-ok">✓</span> Added{" "}
        <span className="font-mono font-medium">{name}</span>
      </h2>
      <p>
        <b className="font-semibold">Run this on the new server</b>{" "}
        <span className="text-mut">
          in the unpacked release, next to{" "}
          <code className="font-mono">dock</code> and{" "}
          <code className="font-mono">deploy/</code>. The join token in it works
          once, expires in {until(added.expires_at)} and is not shown again.
        </span>
      </p>
      <Secret label="Install command" text={added.command} />
      <Secret label="Join token" text={added.token} />
      <p className="text-mut">
        The new server dials this control plane on port 7443, so it must reach
        it over Tailscale or the LAN; nothing has to reach the new server.
      </p>
      <p>
        <StateText state={serverState(status)} />{" "}
        <span className="text-mut">{waiting[status]}</span>
      </p>
      <div>
        <Button small onClick={onDone}>
          Done
        </Button>
      </div>
    </section>
  );
}
