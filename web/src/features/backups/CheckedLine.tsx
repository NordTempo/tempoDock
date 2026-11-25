import { useIsOperator } from "../../data/queries";
import type { ServiceBackups, ServiceRef } from "../../data/types";
import { CliHint } from "../../ui/CliHint";
import { ago, clock } from "../../ui/format";
import { Button, ErrorText, Glyph } from "../../ui/kit";
import { toneText } from "../../ui/status";
import { checkedParts, type Words } from "./checked";
import { useRunDrill } from "./data";

/** The app's times for checked backups: "3d ago" and "Sunday 03:00". */
export const checkedWords: Words = {
  ago,
  at: (iso) =>
    `${new Date(iso).toLocaleDateString("en", { weekday: "long" })} ${clock(iso)}`,
};

/**
 * Whether a service's backups can be read back (ADR 0096), as the
 * "Checked" row of the Backups section (a <dt> and <dd> in its list): the
 * repository's weekly check, a template database's restore drill, and
 * when the next ones run. Operators can run the drill now from here.
 * Nothing renders while there is nothing to say; the row follows backup.*
 * audit events (data/live.ts), nothing polls.
 */
export function CheckedLine({
  serviceRef: ref,
  backups,
}: {
  serviceRef: ServiceRef;
  backups: ServiceBackups;
}) {
  const operator = useIsOperator();
  const drill = useRunDrill();
  const parts = checkedParts(backups, checkedWords);
  if (parts.length === 0) return null;
  // Template databases have drills; a volume's check covers it.
  const drillable = backups.restorable && backups.kind !== "volume";
  return (
    <>
      <dt>Checked</dt>
      <dd className="flex flex-wrap items-baseline gap-x-5 gap-y-1">
        {parts.map(({ key, hint, ...state }) => (
          // Plain coloured text, not the kit's state chip: errors run long
          // and must wrap.
          <span
            key={key}
            className={`min-w-0 break-words ${toneText[state.tone]}`}
          >
            {state.glyph && (
              <>
                <Glyph char={state.glyph} />{" "}
              </>
            )}
            {state.label}
            {hint && (
              <>
                {" "}
                <code className="font-mono text-xs text-mut">{hint}</code>
              </>
            )}
          </span>
        ))}
        {operator && drillable && (
          <CliHint
            cli={`dock backup drill ${ref.service} -e ${ref.environment}`}
            className="act"
          >
            <Button
              small
              title="Restore the newest backup into a throwaway copy, now"
              disabled={drill.isPending || backups.drill?.status === "running"}
              onClick={() => drill.mutate(ref)}
            >
              Check now
            </Button>
          </CliHint>
        )}
        <ErrorText error={drill.error} />
      </dd>
    </>
  );
}
