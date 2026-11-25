import { Fragment, useMemo, useState } from "react";
import { ago, pct, plural, size } from "../../ui/format";
import { Act, Button, ErrorText, Panel } from "../../ui/kit";
import { notify } from "../../ui/notice";
import { type DiskPart, usePruneServer, useServerDisk } from "./disk";

type Group = DiskPart["group"];

/** The groups in the order the bar, legend and table show them. */
const groups: { group: Group; label: string; dot: string }[] = [
  { group: "apps", label: "App data", dot: "bg-disk-apps" },
  { group: "images", label: "Images kept", dot: "bg-disk-images" },
  { group: "reclaimable", label: "Reclaimable", dot: "bg-disk-reclaimable" },
  { group: "build", label: "Build cache", dot: "bg-disk-build" },
  { group: "dock", label: "Dock", dot: "bg-disk-dock" },
  { group: "other", label: "Other", dot: "bg-disk-other" },
];

/**
 * The Prune button on the server page's Disk row, when part of the disk
 * can be freed: asks once ("Prune 1.4G now" or Cancel), then says what
 * was freed in a notice. `dock server prune`.
 */
export function PruneAction({ server }: { server: string }) {
  const d = useServerDisk(server).data;
  const prune = usePruneServer(server);
  const [confirming, setConfirming] = useState(false);
  if (!d || d.reclaimable_bytes <= 0) return null;
  const cli = `dock server prune ${server}`;
  if (!confirming) {
    return (
      <Button small onClick={() => setConfirming(true)}>
        Prune {size(d.reclaimable_bytes)}
      </Button>
    );
  }
  return (
    <span className="flex items-center gap-2">
      <Act
        small
        danger
        cli={cli}
        disabled={prune.isPending}
        onClick={() =>
          prune.mutate(undefined, {
            onSuccess: (done) =>
              notify(
                done.image_bytes + done.cache_bytes > 0
                  ? `Freed ${size(done.image_bytes + done.cache_bytes)}: ${plural(done.images_removed, "image")} (${size(done.image_bytes)}), ${size(done.cache_bytes)} of build cache.${done.error ? ` Part of it failed: ${done.error}` : ""}`
                  : "Nothing was freed: no image or cache could go.",
                done.error ? "fail" : "ok",
              ),
            onError: (e) => notify(e.message, "fail"),
            onSettled: () => setConfirming(false),
          })
        }
      >
        {prune.isPending
          ? "Pruning…"
          : `Prune ${size(d.reclaimable_bytes)} now`}
      </Act>
      <Button small onClick={() => setConfirming(false)}>
        Cancel
      </Button>
    </span>
  );
}

/**
 * The Disk row's few words: "11G of 40G · 1.4G reclaimable", from the
 * same reading as the breakdown.
 */
export function DiskSummary({ server }: { server: string }) {
  const disk = useServerDisk(server);
  const d = disk.data;
  if (!d) return <>{disk.isPending ? "reading the disk" : "not read"}</>;
  return (
    <>
      {size(d.used_bytes)} of {size(d.total_bytes)}
      {d.reclaimable_bytes > 0 && (
        <span className="text-warn">
          {" "}
          · {size(d.reclaimable_bytes)} reclaimable
        </span>
      )}
    </>
  );
}

/**
 * Where a server's disk goes (ADR 0035), the Disk row opened: one bar
 * split by group, a table of the groups that open to show their parts,
 * and when it was read. Prune is on the row (PruneAction). Read when the
 * page opens, not with it: the read takes seconds.
 */
export function DiskBreakdown({ server }: { server: string }) {
  const disk = useServerDisk(server);
  const [open, setOpen] = useState<ReadonlySet<Group>>(() => new Set());
  const d = disk.data;
  const byGroup = useMemo(
    () =>
      groups.map((g) => {
        const parts = (d?.parts ?? []).filter((p) => p.group === g.group);
        return {
          ...g,
          parts,
          bytes: parts.reduce((n, p) => n + p.bytes, 0),
          reclaimable: parts.reduce((n, p) => n + p.reclaimable, 0),
        };
      }),
    [d],
  );
  const toggle = (g: Group) =>
    setOpen((was) => {
      const next = new Set(was);
      if (!next.delete(g)) next.add(g);
      return next;
    });

  const total = d?.total_bytes ?? 0;
  const used = d?.used_bytes ?? 0;
  const share = (n: number) => (total ? (100 * n) / total : 0);

  return (
    <div>
      {d && (
        <p className="mb-2 text-xs text-mut">
          Read {ago(d.read_at)}
          {d.refreshing ? ", reading again" : ""}.
        </p>
      )}
      {disk.isPending && (
        <p className="text-mut">
          Reading the disk: Docker sizes every image and volume, which takes a
          few seconds.
        </p>
      )}
      <ErrorText error={disk.error} />
      {d?.error && <p className="mb-2 text-fail">{d.error}</p>}
      {d && (
        <>
          <div className="flex h-2.5 overflow-hidden rounded-tag bg-line">
            {byGroup.map((g) => (
              <i
                key={g.group}
                className={`block h-full ${g.dot}`}
                style={{ width: `${share(g.bytes)}%` }}
              />
            ))}
          </div>
          <div className="mt-2.5 mb-3.5 flex flex-wrap gap-x-5 gap-y-1.5 text-xs">
            {byGroup.map(
              (g) =>
                g.bytes > 0 && (
                  <span key={g.group}>
                    <Dot className={g.dot} />
                    {g.label}{" "}
                    <span className="font-mono text-fg">{size(g.bytes)}</span>
                  </span>
                ),
            )}
            {total > 0 && (
              <span>
                <Dot className="bg-line outline outline-edge" />
                Free{" "}
                <span className="font-mono text-fg">
                  {size(Math.max(0, total - used))}
                </span>
              </span>
            )}
          </div>
          <Panel>
            <table>
              <thead>
                <tr>
                  <th>Part</th>
                  <th className="text-right">Disk</th>
                  <th className="text-right">Of used</th>
                  <th className="text-right">Reclaimable</th>
                  <th className="w-full" />
                </tr>
              </thead>
              <tbody>
                {byGroup.map((g) => {
                  if (!g.parts.length) return null;
                  const opened = open.has(g.group);
                  return (
                    <Fragment key={g.group}>
                      <tr>
                        <td>
                          <button
                            type="button"
                            aria-expanded={opened}
                            className="hover:text-fg"
                            onClick={() => toggle(g.group)}
                          >
                            <span aria-hidden className="text-faint">
                              {opened ? "▾" : "▸"}
                            </span>{" "}
                            <Dot className={g.dot} />
                            {g.label}
                          </button>
                        </td>
                        <td className="text-right font-mono">
                          {size(g.bytes)}
                        </td>
                        <td className="text-right font-mono text-mut">
                          {used ? pct((100 * g.bytes) / used) : ""}
                        </td>
                        <Reclaimable bytes={g.reclaimable} />
                        <td className="text-mut">
                          {plural(g.parts.length, "part")}
                        </td>
                      </tr>
                      {opened &&
                        g.parts.map((p) => (
                          <tr key={`${p.name}/${p.detail ?? ""}`}>
                            <td className="pl-9 font-mono text-sec">
                              {p.name}
                            </td>
                            <td className="text-right font-mono text-sec">
                              {size(p.bytes)}
                            </td>
                            <td className="text-right font-mono text-faint">
                              {used ? pct((100 * p.bytes) / used) : ""}
                            </td>
                            <Reclaimable bytes={p.reclaimable} />
                            <td className="text-mut">{p.detail}</td>
                          </tr>
                        ))}
                    </Fragment>
                  );
                })}
              </tbody>
            </table>
          </Panel>
        </>
      )}
    </div>
  );
}

function Reclaimable({ bytes }: { bytes: number }) {
  return (
    <td className="text-right font-mono">
      {bytes > 0 ? (
        <span className="text-warn-text">{size(bytes)}</span>
      ) : (
        <span className="text-faint">·</span>
      )}
    </td>
  );
}

function Dot({ className }: { className: string }) {
  return (
    <span
      aria-hidden
      className={`mr-1.5 inline-block size-2 rounded-full ${className}`}
    />
  );
}
