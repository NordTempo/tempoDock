import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { ago, size } from "../../ui/format";
import { Act, Button, ErrorText, Panel, SectionHead } from "../../ui/kit";
import { storageQuery } from "../storage/data";
import { type BackupSettings, useMoveBackups } from "./settings";

// Where the backups are and the move to a storage connection (ADR 0039),
// on the console's Backups page: each repository holding backups restores
// can use, with the move's progress per source.

const statusText: Record<string, string> = {
  waiting: "waiting",
  copying: "copying",
  copied: "copied",
  emptied: "moved, emptied",
  failed: "failed",
};

export function BackupStorage({
  settings,
  moving,
  onMoved,
}: {
  settings: BackupSettings;
  moving: boolean;
  onMoved: () => void;
}) {
  const m = settings.move;
  const repos = settings.repositories ?? [];
  const progress = new Map(
    (m?.running ? (m.sources ?? []) : []).map((s) => [
      `${s.repository}/${s.server}`,
      s,
    ]),
  );
  return (
    <section aria-labelledby="storage" className="flex flex-col gap-2">
      <SectionHead id="storage">Storage</SectionHead>
      {repos.length === 0 ? (
        <p className="text-mut">Local disk on each server.</p>
      ) : (
        <Panel>
          <table>
            <thead>
              <tr>
                <th>Repository</th>
                <th>Server</th>
                <th className="text-right">Snapshots</th>
                <th className="text-right">Size</th>
                <th>Move</th>
                <th className="w-full" />
              </tr>
            </thead>
            <tbody>
              {repos.map((r) => {
                const p = progress.get(`${r.repository}/${r.server ?? ""}`);
                return (
                  <tr key={`${r.repository}/${r.server ?? ""}`}>
                    <td className="py-2 font-mono">
                      {r.repository}
                      {r.repository === "local" && (
                        <span className="font-sans text-warn">
                          {" "}
                          not off-box
                        </span>
                      )}
                    </td>
                    <td className="py-2 font-mono">
                      {r.server || (
                        <span className="font-sans text-mut">bucket</span>
                      )}
                    </td>
                    <td className="py-2 text-right font-mono">{r.snapshots}</td>
                    <td className="py-2 text-right font-mono">
                      {size(r.bytes)}
                    </td>
                    <td className="py-2">
                      {p ? (
                        <span className="text-warn-text">
                          {statusText[p.status] ?? p.status}
                        </span>
                      ) : r.current ? (
                        <span className="text-mut">new backups go here</span>
                      ) : (
                        <span className="text-faint">·</span>
                      )}
                    </td>
                    <td />
                  </tr>
                );
              })}
            </tbody>
          </table>
        </Panel>
      )}
      <MoveResult settings={settings} />
      {moving && <MoveForm settings={settings} onDone={onMoved} />}
    </section>
  );
}

/** How the latest move went, once it ended. */
function MoveResult({ settings }: { settings: BackupSettings }) {
  const m = settings.move;
  if (!m) return null;
  if (m.running) {
    return (
      <p className="text-warn-text">
        Moving the backups to {m.to}, started {ago(m.started_at)}. Backups and
        restores wait until it finishes.
      </p>
    );
  }
  const notes = (m.sources ?? []).filter((s) => s.error);
  return (
    <div className={m.error ? "text-fail" : "text-mut"}>
      {m.error
        ? `Moving the backups to ${m.to} failed, they stay where they were: ${m.error}`
        : m.moved > 0
          ? `Moved ${m.moved} backups to ${m.to} ${m.finished_at ? ago(m.finished_at) : ""}.`
          : `New backups go to ${m.to}; there were none to move.`}
      {notes.map((s) => (
        <span
          key={`${s.repository}/${s.server}`}
          className="block text-warn-text"
        >
          {s.repository} on {s.server}: {s.error}
        </span>
      ))}
    </div>
  );
}

/** Picks the storage connection and folder, and starts the move. */
function MoveForm({
  settings,
  onDone,
}: {
  settings: BackupSettings;
  onDone: () => void;
}) {
  const storage = useQuery(storageQuery()).data ?? [];
  const move = useMoveBackups();
  const [picked, setPicked] = useState<string>();
  const [path, setPath] = useState(settings.path || "dock-backups");
  const to = picked ?? storage[0]?.name ?? "";
  const snapshots = (settings.repositories ?? [])
    .filter((r) => !r.current)
    .reduce((n, r) => n + r.snapshots, 0);
  return (
    <form
      aria-label="Move backups"
      className="flex max-w-[760px] flex-col gap-3 border-t border-line pt-3.5"
      onKeyDown={(e) => e.key === "Escape" && onDone()}
      onSubmit={(e) => {
        e.preventDefault();
        move.mutate({ storage: to, path: path.trim() }, { onSuccess: onDone });
      }}
    >
      {storage.length === 0 ? (
        <p className="text-mut">
          Add a bucket on the <Link to="/console/storage">Storage</Link> page
          first.
        </p>
      ) : (
        <div className="flex flex-wrap gap-3">
          <label className="flex flex-[2_1_240px] flex-col gap-1.5">
            <span>
              Move to <span className="text-mut">a storage connection</span>
            </span>
            <select
              value={to}
              onChange={(e) => setPicked(e.target.value)}
              className="field py-[7px]"
            >
              {storage.map((s) => (
                <option key={s.name} value={s.name}>
                  {s.name} · bucket {s.bucket}
                </option>
              ))}
            </select>
          </label>
          <label className="flex flex-[1_1_160px] flex-col gap-1.5">
            <span>Folder in the bucket</span>
            <input
              value={path}
              onChange={(e) => setPath(e.target.value)}
              className="field py-[7px] font-mono"
            />
          </label>
        </div>
      )}
      <p className="text-mut">
        {snapshots > 0
          ? `${snapshots} backups are copied there and checked, then deleted where they are now. `
          : ""}
        Restores keep working throughout; backups and restores wait while it
        runs. A failure before the switch changes nothing.
      </p>
      <ErrorText error={move.error} />
      <div className="flex items-center gap-2.5">
        <Act
          primary
          type="submit"
          cli={`dock backup move --storage ${to || "<name>"}${path.trim() && path.trim() !== "dock-backups" ? ` --path ${path.trim()}` : ""}`}
          disabled={!to || move.isPending}
        >
          Move backups
        </Act>
        <Button onClick={onDone}>Cancel</Button>
      </div>
    </form>
  );
}
