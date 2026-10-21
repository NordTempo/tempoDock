import { useApplyChanges, useDiscardChanges } from "../../data/mutations";
import { Button, ErrorText } from "../../ui/kit";

/** "Apply N changes / Discard" for a set of staged changes. */
export function ApplyBar({
  environmentId,
  changeIds,
  note,
  label,
}: {
  environmentId: string;
  changeIds: string[];
  /** What applying will do, e.g. "Creates release r90." */
  note: string;
  /** Overrides the button text. */
  label?: string;
}) {
  const apply = useApplyChanges();
  const discard = useDiscardChanges();
  if (changeIds.length === 0) return null;
  const n = changeIds.length;
  const busy = apply.isPending || discard.isPending;
  return (
    <div className="flex items-center gap-3 py-2.5">
      <Button
        primary
        disabled={busy}
        onClick={() => apply.mutate({ environmentId, changeIds })}
      >
        {label ?? `Apply ${n} ${n === 1 ? "change" : "changes"}`}
      </Button>
      <Button
        disabled={busy}
        onClick={() => discard.mutate({ environmentId, changeIds })}
      >
        Discard
      </Button>
      <span className="text-dim">{note}</span>
      <ErrorText error={apply.error ?? discard.error} />
    </div>
  );
}
