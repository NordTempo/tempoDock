import { useState } from "react";
import type { ServiceRef } from "../../data/types";
import { Dialog } from "../../ui/Dialog";
import { Button, DialogFoot, ErrorText } from "../../ui/kit";
import { autofocus, input, invalid } from "../create/form";
import { hourLabel } from "./backups";
import { useSetPITR } from "./data";
import type { PITR } from "./pitr";

/** What the dialog does: turn on, change the days, or turn off. */
export type PitrMode = "on" | "edit" | "off";

/**
 * Point-in-time restore's one settings dialog (ADR 0093, mock A4
 * confirm), for operators: turning it on says it restarts the database
 * once and stores WAL in proportion to its writes, with the days to keep;
 * Edit changes the days, and Turn off… turns into its own question.
 * Loaded when it opens.
 */
export default function PitrDialog({
  serviceRef: ref,
  pitr,
  hour,
  mode: first,
  onClose,
}: {
  serviceRef: ServiceRef;
  pitr: PITR;
  /** The daily backup's hour, which base backups join. */
  hour?: number;
  mode: PitrMode;
  onClose: () => void;
}) {
  const db = ref.service;
  const [mode, setMode] = useState(first);
  const [days, setDays] = useState(String(pitr.days));
  const set = useSetPITR();
  const n = Number(days);
  const daysError =
    Number.isInteger(n) && n >= 1 && n <= 35 ? undefined : "Keep 1 to 35 days.";
  const env = `-e ${ref.environment}`;
  const done = { onSuccess: onClose };

  if (mode === "off") {
    return (
      <Dialog
        title={`Turn off point-in-time restore for ${db}?`}
        onClose={onClose}
      >
        <form
          className="flex flex-col gap-2 text-xs text-sec"
          onSubmit={(e) => {
            e.preventDefault();
            set.mutate({ ref, enabled: false }, done);
          }}
        >
          <p>
            <b className="font-semibold text-fg">
              {db} restarts once, without archiving.
            </b>{" "}
            Connections drop and reconnect.
          </p>
          <p>
            Restores reach the daily dumps and snapshots again; what is archived
            so far ages out.
          </p>
          <DialogFoot
            cli={`dock backup pitr off ${db} ${env}`}
            note={<ErrorText error={set.error} />}
          >
            <Button data-autofocus onClick={onClose}>
              Cancel
            </Button>
            <Button
              type="submit"
              danger
              className="solid"
              disabled={set.isPending}
            >
              Turn off and restart {db}
            </Button>
          </DialogFoot>
        </form>
      </Dialog>
    );
  }

  const on = mode === "on";
  return (
    <Dialog
      title={
        on
          ? `Turn on point-in-time restore for ${db}?`
          : `Point-in-time restore of ${db}`
      }
      onClose={onClose}
    >
      <form
        className="flex flex-col gap-3 text-xs text-sec"
        onSubmit={(e) => {
          e.preventDefault();
          if (daysError) return;
          set.mutate(
            on ? { ref, enabled: true, days: n } : { ref, days: n },
            done,
          );
        }}
      >
        {on && (
          <>
            <p>
              <b className="font-semibold text-fg">
                {db} restarts once to start archiving.
              </b>{" "}
              Connections drop and reconnect.
            </p>
            <p>
              A small archiver runs next to {db} and sends each change off-site
              as it happens; a base backup joins the daily dump
              {hour === undefined ? "" : ` at ${hourLabel(hour)}`}. The archive
              grows with {db}'s writes, for the days kept.
            </p>
          </>
        )}
        <label className="flex flex-wrap items-center gap-2">
          <span className="text-mut">Keep</span>
          <input
            ref={autofocus}
            type="number"
            min={1}
            max={35}
            aria-label="Days to keep"
            aria-invalid={!!daysError}
            value={days}
            onChange={(e) => setDays(e.target.value)}
            className={`${input} w-20 ${invalid(daysError)}`}
          />
          <span className="text-mut">days (1 to 35)</span>
        </label>
        <DialogFoot
          className="mt-1"
          cli={`dock backup pitr on ${db} --days ${days || "N"} ${env}`}
          note={
            set.error ? (
              <ErrorText error={set.error} />
            ) : (
              daysError && <span className="text-fail">{daysError}</span>
            )
          }
        >
          {!on && (
            <Button danger onClick={() => setMode("off")}>
              Turn off…
            </Button>
          )}
          <Button onClick={onClose}>Cancel</Button>
          <button
            type="submit"
            className="btn pri"
            disabled={set.isPending || !!daysError}
          >
            {on ? `Turn on and restart ${db}` : "Save"}
          </button>
        </DialogFoot>
      </form>
    </Dialog>
  );
}
