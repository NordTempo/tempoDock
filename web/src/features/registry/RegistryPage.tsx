import { Link } from "@tanstack/react-router";
import { plural, since, size } from "../../ui/format";
import {
  Act,
  ErrorText,
  Page,
  Panel,
  SectionHead,
  TitleRow,
} from "../../ui/kit";
import { type Registry, usePruneRegistry, useRegistry } from "./data";

// The console's Registry page: what the platform registry stores, how
// pruning went since the daemon started, and the services keeping images
// in it (ADR 0014 section 4).

export function RegistryPage() {
  const registry = useRegistry();
  const prune = usePruneRegistry();
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
          <Act
            cli="dock registry prune"
            disabled={registry.pruning || prune.isPending}
            onClick={() => prune.mutate()}
          >
            {registry.pruning ? "Pruning…" : "Prune now"}
          </Act>
        }
      />
      <ErrorText error={prune.error} />
      <dl className="grid grid-cols-[repeat(auto-fit,minmax(170px,1fr))] border-y border-line">
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
      </dl>
      {registry.last_prune_error && (
        <p className="text-fail">
          Last prune failed: {registry.last_prune_error}
        </p>
      )}
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
                          to="/$project/$environment/$service/releases"
                          params={{
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

function Fact({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex flex-col gap-0.5 border-line px-3 py-2.5 not-last:border-r">
      <dt className="text-2xs text-faint">{label}</dt>
      <dd className="text-md font-semibold">{children}</dd>
    </div>
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
