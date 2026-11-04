import { useQuery } from "@tanstack/react-query";
import type { EnvRef } from "../../data/types";
import { Glyph } from "../../ui/kit";
import { moveQuery, moveRunning } from "./data";

/**
 * One line under an environment's heading while it moves to another
 * server (`movingTo`, from the project overview): where to, the step, and
 * that deploys are refused. Only a moving environment fetches its move.
 * `onDetails` opens the move's progress.
 */
export function MoveStrip({
  env,
  movingTo,
  onDetails,
}: {
  env: EnvRef;
  movingTo?: string;
  onDetails: () => void;
}) {
  const { data: move } = useQuery({ ...moveQuery(env), enabled: !!movingTo });
  if (!movingTo || (move && !moveRunning(move))) return null;
  const to = move?.to ?? movingTo;
  const step = !move
    ? null
    : move.status === "rolling_back"
      ? "rolling back"
      : move.step;
  return (
    <p className="mb-2.5 flex flex-wrap items-center gap-x-3 gap-y-1 rounded-ctl border border-warn-edge bg-prog-bg px-3 py-1.5 text-xs">
      <span className="text-prog">
        <Glyph char="◐" /> Moving to <span className="font-mono">{to}</span>
        {step ? `: ${step}.` : "."}
      </span>
      <span className="text-mut">Deploys are refused until it ends.</span>
      <button
        type="button"
        className="ml-auto text-sec hover:text-fg"
        onClick={onDetails}
      >
        Details
      </button>
    </p>
  );
}
