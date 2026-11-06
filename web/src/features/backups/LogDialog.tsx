import { useQuery } from "@tanstack/react-query";
import type { ServiceRef } from "../../data/types";
import { Dialog } from "../../ui/Dialog";
import { clock, day } from "../../ui/format";
import { ErrorText } from "../../ui/kit";
import { backupLogQuery, snapshotLogQuery } from "./data";
import { pointName, type RestorePoint } from "./points";

/**
 * What a backup run's or a failed snapshot's job printed (`dock backup
 * log`), read when it opens. Loaded lazily.
 */
export default function LogDialog({
  serviceRef: ref,
  point,
  onClose,
}: {
  serviceRef: ServiceRef;
  point: RestorePoint;
  onClose: () => void;
}) {
  const log = useQuery(
    point.kind === "backup"
      ? backupLogQuery(ref, point.run.id)
      : snapshotLogQuery(ref, point.snapshot.id),
  );
  const flags = `-s ${ref.service} -e ${ref.environment}`;
  // The CLI has no snapshot log yet.
  const cli =
    point.kind === "backup" ? `dock backup log ${point.run.id} ${flags}` : "";
  return (
    <Dialog
      title={`${pointName(point)}, ${day(point.at)} ${clock(point.at)}`}
      onClose={onClose}
      wide
    >
      <div className="flex flex-col gap-3">
        {point.state === "failed" && point.detail && (
          <p className="text-fail">{point.detail}</p>
        )}
        {log.isPending ? (
          <p className="text-mut">Loading the log.</p>
        ) : log.error ? (
          <ErrorText error={log.error} />
        ) : log.data.output ? (
          <pre className="max-h-[60vh] overflow-auto rounded-ctl border border-line bg-side p-3 font-mono text-xs whitespace-pre-wrap">
            {log.data.output}
          </pre>
        ) : (
          <p className="text-mut">No log was kept for this run.</p>
        )}
        {cli && (
          <code className="border-t border-line pt-2.5 font-mono text-2xs text-faint">
            {cli}
          </code>
        )}
      </div>
    </Dialog>
  );
}
