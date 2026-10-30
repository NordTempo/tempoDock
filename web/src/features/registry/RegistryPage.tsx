import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useOrg } from "../../shell/location";
import { plural, since, size } from "../../ui/format";
import {
  Act,
  Button,
  ErrorText,
  Fact,
  Facts,
  Page,
  Panel,
  SectionHead,
  TitleRow,
} from "../../ui/kit";
import { storageQuery } from "../storage/data";
import {
  type Registry,
  useMigrateRegistry,
  usePruneRegistry,
  useRegistry,
} from "./data";

// The console's Registry page: what the platform registry stores and
// where (ADR 0034), how pruning and moves went since the daemon started,
// and the services keeping images in it (ADR 0014 section 4).

export function RegistryPage() {
  const org = useOrg();
  const registry = useRegistry();
  const prune = usePruneRegistry();
  const [moving, setMoving] = useState(false);
  if (!registry.enabled) {
    return (
      <Page>
        <TitleRow title="Registry" />
        <p className="text-mut">
          This Dock has no registry: images stay on the server that built them.
          A second server needs one; install.sh sets it up with{" "}
          <span className="font-mono">REGISTRY=1</span>.
        </p>
      </Page>
    );
  }
  const services = registry.services ?? [];
  const kept = services.reduce((n, s) => n + s.kept, 0);
  const archived = services.filter((s) => s.archived).length;
  return (
    <Page>
      <TitleRow
        title="Registry"
        context={
          <>
            <span className="font-mono">{registry.host}</span> on the control
            plane · every server pulls through its node link
          </>
        }
        actions={
          <>
            {!moving && (
              <Button
                disabled={registry.migration?.running}
                onClick={() => setMoving(true)}
              >
                Move images
              </Button>
            )}
            <Act
              cli="dock registry prune"
              disabled={
                registry.pruning ||
                prune.isPending ||
                registry.migration?.running
              }
              onClick={() => prune.mutate()}
            >
              {registry.pruning ? "Pruning…" : "Prune now"}
            </Act>
          </>
        }
      />
      <ErrorText error={prune.error} />
      {moving && (
        <MoveImages registry={registry} onClose={() => setMoving(false)} />
      )}
      <Facts>
        <Fact label="Storage">
          {registry.storage === "local" ? (
            "this server's disk"
          ) : (
            <Link to="/console/storage" className="font-mono">
              {registry.storage}
            </Link>
          )}
        </Fact>
        <Fact label="Stored">
          {registry.stored_bytes ? size(registry.stored_bytes) : "unknown"}
        </Fact>
        <Fact label="Images kept">
          {kept}
          <span className="text-xs font-normal text-mut">
            {" "}
            in {plural(services.length - archived, "service")}
          </span>
        </Fact>
        <Fact label="Rollback window">
          {plural(registry.keep_releases, "release")}
        </Fact>
        <Fact label="Last prune">
          <LastPrune registry={registry} />
        </Fact>
      </Facts>
      {registry.last_prune_error && (
        <p className="text-fail">
          Last prune failed: {registry.last_prune_error}
        </p>
      )}
      <Migration registry={registry} />
      {registry.collect_pending && (
        <p className="text-warn-text">
          Deleted images' layers are still on disk; the next prune collects
          them.
        </p>
      )}
      <section aria-labelledby="services">
        <SectionHead
          id="services"
          aside="each service's rollback window and active release keep their images; the rest go with the next prune"
        >
          Services
        </SectionHead>
        {services.length === 0 ? (
          <p className="text-mut">No images pushed yet.</p>
        ) : (
          <Panel>
            <table>
              <thead>
                <tr>
                  <th>Service</th>
                  <th>Repository</th>
                  <th>Kept</th>
                  <th>Newest</th>
                  <th>Active</th>
                </tr>
              </thead>
              <tbody>
                {services.map((s) => (
                  <tr key={s.repository}>
                    <td className="py-2">
                      {s.archived ? (
                        <span className="text-mut">
                          {s.project} / {s.environment} / {s.service}
                        </span>
                      ) : (
                        <Link
                          to="/$org/$project/$environment/$service/releases"
                          params={{
                            org,
                            project: s.project,
                            environment: s.environment,
                            service: s.service,
                          }}
                        >
                          {s.project} / {s.environment} / {s.service}
                        </Link>
                      )}
                    </td>
                    <td className="py-2 font-mono text-mut">{s.repository}</td>
                    <td className="py-2 font-mono">
                      {s.archived ? (
                        <span className="font-sans text-warn">archived</span>
                      ) : (
                        s.kept
                      )}
                    </td>
                    <td className="py-2 font-mono">#{s.newest}</td>
                    <td className="w-full py-2 font-mono">
                      {s.active ? (
                        `#${s.active}`
                      ) : (
                        <span className="text-faint">·</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Panel>
        )}
      </section>
    </Page>
  );
}

function LastPrune({ registry: r }: { registry: Registry }) {
  if (r.pruning) return <span className="text-warn">running</span>;
  if (!r.last_prune_at) {
    return (
      <span className="text-sm font-normal text-mut">not since start</span>
    );
  }
  return (
    <>
      {since(r.last_prune_at)} ago
      <span className="text-xs font-normal text-mut">
        {" "}
        · {plural(r.last_pruned, "image")} deleted
      </span>
    </>
  );
}

/** The latest move of the images, while it runs and once it ended. */
function Migration({ registry: r }: { registry: Registry }) {
  const m = r.migration;
  if (!m) return null;
  if (m.running) {
    return (
      <p className="text-warn-text">
        Moving the images from {m.from} to {m.to} since {since(m.started_at)}{" "}
        ago. The registry serves them read-only; builds and pulls wait.
      </p>
    );
  }
  if (m.error) {
    return (
      <p className="text-fail">
        Moving the images from {m.from} to {m.to} failed: {m.error}
      </p>
    );
  }
  return (
    <p className="text-mut">
      Moved the images from {m.from} to {m.to}{" "}
      {m.finished_at && `${since(m.finished_at)} ago`}:{" "}
      {plural(m.objects, "object")}, {size(m.bytes)}.
    </p>
  );
}

/** Picks where the images go: this server's disk or a storage connection. */
function MoveImages({
  registry,
  onClose,
}: {
  registry: Registry;
  onClose: () => void;
}) {
  const storage = useQuery(storageQuery()).data ?? [];
  const migrate = useMigrateRegistry();
  const targets = ["local", ...storage.map((s) => s.name)].filter(
    (n) => n !== registry.storage,
  );
  // The connections may load after the form opens: the first one counts
  // as chosen until the admin picks another.
  const [picked, setTo] = useState<string>();
  const to = picked ?? targets[0] ?? "";
  const cli =
    to === "local"
      ? "dock registry migrate --to local"
      : `dock registry migrate --to s3 --storage ${to}`;
  return (
    <form
      aria-label="Move images"
      className="flex max-w-[760px] flex-col gap-3 border-t border-line pt-3.5"
      onKeyDown={(e) => e.key === "Escape" && onClose()}
      onSubmit={(e) => {
        e.preventDefault();
        migrate.mutate(
          to === "local" ? { to: "local" } : { to: "s3", storage: to },
          { onSuccess: onClose },
        );
      }}
    >
      {targets.length === 0 ? (
        <p className="text-mut">
          Add a bucket on the <Link to="/console/storage">Storage</Link> page
          first.
        </p>
      ) : (
        <label className="flex max-w-[360px] flex-col gap-1.5">
          <span>Move to</span>
          <select
            value={to}
            onChange={(e) => setTo(e.target.value)}
            className="field py-[7px]"
          >
            {targets.map((t) => (
              <option key={t} value={t}>
                {t === "local" ? "this server's disk" : t}
              </option>
            ))}
          </select>
        </label>
      )}
      <p className="text-mut">
        The registry serves the images read-only while they are copied, and
        builds and pulls wait. Once the copy is checked the registry switches
        over and the old copy is deleted.
      </p>
      <ErrorText error={migrate.error} />
      <div className="flex items-center gap-2.5">
        <Act
          primary
          type="submit"
          cli={cli}
          disabled={!to || migrate.isPending}
        >
          Move images
        </Act>
        <Button onClick={onClose}>Cancel</Button>
      </div>
    </form>
  );
}
