import { useState } from "react";
import { Dialog } from "../../ui/Dialog";
import { Button, DialogFoot, ErrorText } from "../../ui/kit";
import { type HostCheck, useDismissFinding } from "./data";

/**
 * Dismisses a host check finding with a reason other operators see: it
 * stops counting and alerting until restored (ADR 0109). Loads when
 * opened.
 */
export default function DismissFinding({
  server,
  check,
  onClose,
}: {
  server: string;
  check: HostCheck;
  onClose: () => void;
}) {
  const [reason, setReason] = useState("");
  const dismiss = useDismissFinding();
  const ready = reason.trim() !== "" && !dismiss.isPending;
  return (
    <Dialog title={`Dismiss "${check.title}" on ${server}?`} onClose={onClose}>
      <form
        className="flex flex-col gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (!ready) return;
          dismiss.mutate(
            { server, finding: check.key, reason: reason.trim() },
            { onSuccess: onClose },
          );
        }}
      >
        <p className="text-xs text-sec">
          It stops counting and alerting until restored, also if it goes away
          and comes back. Other operators see why.
        </p>
        <label className="flex flex-col gap-1.5 text-xs text-mut">
          Why it doesn't need fixing
          <input
            data-autofocus
            className="field"
            value={reason}
            maxLength={500}
            autoComplete="off"
            placeholder="Hetzner's firewall allows 22, 80 and 443 only"
            onChange={(e) => setReason(e.target.value)}
          />
        </label>
        <ErrorText error={dismiss.error} />
        <DialogFoot
          cli={`dock server checks dismiss ${shellArg(server)} ${shellArg(check.key)} --reason ${shellArg(reason.trim() || "...")}`}
        >
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" primary disabled={!ready}>
            Dismiss
          </Button>
        </DialogFoot>
      </form>
    </Dialog>
  );
}

/** One shell argument, single-quoted where it needs to be, so a copied command passes it exactly. */
function shellArg(s: string) {
  return /^[\w@%+=:,./-]+$/.test(s) ? s : `'${s.replaceAll("'", `'\\''`)}'`;
}
