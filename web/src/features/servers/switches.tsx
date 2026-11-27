import { type ReactNode, useState } from "react";
import { useUpdateServer } from "../../data/mutations";
import { useServers } from "../../data/queries";
import type { EnvironmentKind, Server, ServerPatch } from "../../data/types";
import { Check, Confirm } from "../../ui/kit";
import { kindLabel } from "../../ui/status";
import { Saved } from "../create/form";

/**
 * A switch turned off, waiting for a yes. `last` when this server is the
 * only one taking that kind of work, so turning it off stops it.
 */
type Ask = {
  patch: ServerPatch;
  action: string;
  cli: string;
  text: ReactNode;
  last?: boolean;
};

/** What a server builds images for: the builder switch and its kinds. */
type Builds = "off" | "non_production" | "production" | "all";

const buildsOptions = [
  "off",
  "non_production",
  "production",
  "all",
] as const satisfies readonly Builds[];

const buildsLabel = {
  off: "nothing",
  non_production: "non-production",
  production: "production",
  all: "all environments",
} as const satisfies Record<Builds, string>;

function buildsOf(s: Server): Builds {
  if (!s.builder) return "off";
  if (s.builds_production && s.builds_non_production) return "all";
  return s.builds_production ? "production" : "non_production";
}

/**
 * Whether another server still takes builds and scans: a revoked one
 * keeps its switches but gets no calls (canBuild on the daemon).
 */
const builds = (s: Server) => s.builder && s.status !== "revoked";

const buildsProduction = (b: Builds) => b === "production" || b === "all";

const scansField = {
  production: "scans_production",
  non_production: "scans_non_production",
} as const satisfies Record<EnvironmentKind, keyof Server>;

/** Whether a Scans choice covers kind. */
const scansKind = (b: Builds, kind: EnvironmentKind) =>
  b === "all" || b === kind;

/** Which kinds' images a builder scans: the same choices as Builds. */
function scansOf(s: Server): Builds {
  if (s.scans_production && s.scans_non_production) return "all";
  if (s.scans_production) return "production";
  return s.scans_non_production ? "non_production" : "off";
}

const acceptsField = {
  production: "accepts_production",
  non_production: "accepts_non_production",
} as const satisfies Record<EnvironmentKind, keyof Server>;

/**
 * An admin's switches for one server: which kinds it accepts, whether it
 * takes new placements, which kinds it builds images for (apart from
 * what it accepts: a staging homelab can build production's images) and,
 * on a builder, which kinds' images it scans (apart from what it builds:
 * each scanner keeps a vulnerability database of a few GB). Every change asks
 * first (the `confirm` node, which the caller places), naming its effect,
 * as a danger when no other server does that work. A switch that would
 * leave production environments nowhere to run is disabled, with why.
 * Each switch says Saved beside it once its change went through.
 */
export function useServerSwitches(server: Server) {
  const update = useUpdateServer();
  const servers = useServers();
  const [ask, setAsk] = useState<Ask | null>(null);
  const { name } = server;
  const apply = (patch: ServerPatch) =>
    update.mutate({ name, patch }, { onSuccess: () => setAsk(null) });
  // Saved beside the switches the last change was for.
  const saved = (...fields: (keyof ServerPatch)[]) => (
    <Saved
      ok={
        update.isSuccess &&
        fields.some((f) => f in (update.variables?.patch ?? {}))
      }
    />
  );

  const kindSwitch = (kind: EnvironmentKind) => {
    const field = acceptsField[kind];
    const words = kindLabel[kind];
    // Where new environments of this kind can still go.
    const others = servers.filter(
      (s) => s.name !== name && s[field] && !s.disabled_for_placement,
    );
    const lastProduction =
      kind === "production" && server[field] && others.length === 0;
    return (
      <Check
        label={words}
        checked={server[field]}
        disabled={update.isPending || lastProduction}
        reason={lastProduction ? "The only production server." : undefined}
        onChange={(on) => {
          if (on)
            return setAsk({
              patch: { [field]: true },
              action: `Accept ${words}`,
              cli: `dock server update ${name} --${words}`,
              text: (
                <>
                  Place {words} environments on{" "}
                  <b className="font-mono font-medium">{name}</b>?{" "}
                  <span className="text-mut">
                    New ones can land here; environments elsewhere stay where
                    they are.
                  </span>
                </>
              ),
            });
          setAsk({
            patch: { [field]: false },
            action: `Stop ${words}`,
            cli: `dock server update ${name} --no-${words}`,
            last: others.length === 0,
            text: (
              <>
                Stop placing {words} environments on{" "}
                <b className="font-mono font-medium">{name}</b>?{" "}
                <span className="text-mut">
                  Environments already there stay.{" "}
                  {others.length
                    ? `New ones go to ${others.map((s) => s.name).join(", ")}.`
                    : ""}
                </span>
                {others.length === 0 && (
                  <span className="mt-2 block text-warn-text">
                    It is the only server that takes {words} environments: new
                    ones will have nowhere to run until another server takes
                    them.
                  </span>
                )}
              </>
            ),
          });
        }}
      />
    );
  };

  // Production goes only where placement is open: closing the last open
  // server would leave new projects nowhere to run.
  const lastPlacement =
    !server.disabled_for_placement &&
    !servers.some((s) => s.name !== name && !s.disabled_for_placement);

  return {
    accepts: (
      <span className="inline-flex flex-wrap items-center gap-x-3">
        {kindSwitch("production")}
        {kindSwitch("non_production")}
        {saved("accepts_production", "accepts_non_production")}
      </span>
    ),
    placement: (
      <span className="inline-flex flex-wrap items-center gap-x-3">
        <Check
          label="new placements"
          checked={!server.disabled_for_placement}
          disabled={update.isPending || lastPlacement}
          reason={lastPlacement ? "The only open server." : undefined}
          onChange={(on) => {
            if (on)
              return setAsk({
                patch: { disabled_for_placement: false },
                action: "Enable placement",
                cli: `dock server update ${name} --enable-placement`,
                text: (
                  <>
                    Place new projects on{" "}
                    <b className="font-mono font-medium">{name}</b> again?{" "}
                    <span className="text-mut">
                      New projects can land here.
                    </span>
                  </>
                ),
              });
            const last = lastPlacement;
            setAsk({
              patch: { disabled_for_placement: true },
              action: "Disable placement",
              cli: `dock server update ${name} --disable-placement`,
              last,
              text: (
                <>
                  Stop placing new projects on{" "}
                  <b className="font-mono font-medium">{name}</b>?{" "}
                  <span className="text-mut">
                    What is there stays, and its projects keep adding
                    environments here. Admins can still pin projects to it.
                  </span>
                  {last && (
                    <span className="mt-2 block text-warn-text">
                      It is the only server taking new projects: new ones will
                      have nowhere to go until another server takes them.
                    </span>
                  )}
                </>
              ),
            });
          }}
        />
        {saved("disabled_for_placement")}
      </span>
    ),
    builder: (
      <span className="inline-flex flex-wrap items-center gap-x-3">
        <span>
          <span className="text-mut">Builds</span>{" "}
          <select
            aria-label={`What ${name} builds`}
            value={buildsOf(server)}
            disabled={update.isPending}
            onChange={(e) => {
              const to =
                buildsOptions.find((b) => b === e.target.value) ?? "off";
              const was = buildsOf(server);
              // Builders left for production when this one stops.
              const prodOthers = servers.filter(
                (s) => s.name !== name && builds(s) && s.builds_production,
              );
              const losesProduction =
                buildsProduction(was) && !buildsProduction(to);
              if (to === "off") {
                const others = servers.filter(
                  (s) => s.name !== name && builds(s),
                );
                const noProduction =
                  losesProduction &&
                  prodOthers.length === 0 &&
                  others.length > 0;
                return setAsk({
                  patch: { builder: false },
                  action: "Stop building",
                  cli: `dock server update ${name} --no-builder`,
                  last: others.length === 0 || noProduction,
                  text: (
                    <>
                      Stop building images on{" "}
                      <b className="font-mono font-medium">{name}</b>?{" "}
                      <span className="text-mut">
                        Builds in progress finish.{" "}
                        {others.length
                          ? `New builds go to ${others.map((s) => s.name).join(", ")}.`
                          : "No other server builds images."}
                      </span>
                      {noProduction && (
                        <span className="mt-2 block text-warn-text">
                          No other server builds production images: production
                          deploys from source will fail until one does.
                        </span>
                      )}
                    </>
                  ),
                });
              }
              setAsk({
                patch: {
                  builder: true,
                  builds_production: buildsProduction(to),
                  builds_non_production: to !== "production",
                },
                action: `Build ${buildsLabel[to]}`,
                cli: `dock server update ${name} --builds ${to === "non_production" ? "non-production" : to}`,
                last: losesProduction && prodOthers.length === 0,
                text: (
                  <>
                    Build images for {buildsLabel[to]} on{" "}
                    <b className="font-mono font-medium">{name}</b>?{" "}
                    <span className="text-mut">
                      {was === "off" &&
                        "New builds may run here and use its CPU and memory. "}
                      {buildsProduction(to) &&
                        !buildsProduction(was) &&
                        "Production builds get production's variables and secrets. "}
                      {!server.local &&
                        "The control plane builds only while no other builder is online."}
                    </span>
                    {losesProduction && prodOthers.length === 0 && (
                      <span className="mt-2 block text-warn-text">
                        No other server builds production images: production
                        deploys from source will fail until one does.
                      </span>
                    )}
                  </>
                ),
              });
            }}
            className="field py-[3px]"
          >
            {buildsOptions.map((b) => (
              <option key={b} value={b}>
                {buildsLabel[b]}
              </option>
            ))}
          </select>
        </span>
        {saved("builder", "builds_production", "builds_non_production")}
      </span>
    ),
    // Only builders scan: elsewhere there is nothing to choose.
    scans: server.builder && (
      <span className="inline-flex flex-wrap items-center gap-x-3">
        <span>
          <span className="text-mut">Scans</span>{" "}
          <select
            aria-label={`What ${name} scans`}
            value={scansOf(server)}
            disabled={update.isPending}
            onChange={(e) => {
              const to =
                buildsOptions.find((b) => b === e.target.value) ?? "off";
              const was = scansOf(server);
              // Each kind this server stops scanning, and the builders
              // that still scan it.
              const lost = (["production", "non_production"] as const)
                .filter((k) => scansKind(was, k) && !scansKind(to, k))
                .map((k) => ({
                  kind: k,
                  others: servers.filter(
                    (s) => s.name !== name && builds(s) && s[scansField[k]],
                  ),
                }));
              const others = [
                ...new Set(lost.flatMap((l) => l.others.map((s) => s.name))),
              ];
              const orphaned = lost
                .filter((l) => l.others.length === 0)
                .map((l) => kindLabel[l.kind]);
              const warning = orphaned.length > 0 && (
                <span className="mt-2 block text-warn-text">
                  No other builder scans {orphaned.join(" or ")} images: they go
                  unscanned until one does.
                </span>
              );
              setAsk({
                patch: {
                  scans_production: buildsProduction(to),
                  scans_non_production: to === "non_production" || to === "all",
                },
                action:
                  to === "off" ? "Stop scanning" : `Scan ${buildsLabel[to]}`,
                cli: `dock server update ${name} --scans ${{ off: "none", non_production: "non-production", production: "production", all: "all" }[to]}`,
                last: orphaned.length > 0,
                text:
                  to === "off" ? (
                    <>
                      Stop scanning images on{" "}
                      <b className="font-mono font-medium">{name}</b>?{" "}
                      <span className="text-mut">
                        Its vulnerability database goes with the next prune (
                        <span className="font-mono">
                          dock server prune {name}
                        </span>
                        ).{" "}
                        {others.length
                          ? `Scans go to ${others.join(", ")}.`
                          : ""}
                      </span>
                      {warning}
                    </>
                  ) : (
                    <>
                      Scan images of {buildsLabel[to]} on{" "}
                      <b className="font-mono font-medium">{name}</b>?{" "}
                      <span className="text-mut">
                        It keeps a vulnerability database of a few GB.{" "}
                        {!server.local &&
                          "The control plane scans only what no other server does."}
                      </span>
                      {warning}
                    </>
                  ),
              });
            }}
            className="field py-[3px]"
          >
            {buildsOptions.map((b) => (
              <option key={b} value={b}>
                {buildsLabel[b]}
              </option>
            ))}
          </select>
        </span>
        {saved("scans_production", "scans_non_production")}
      </span>
    ),
    confirm: ask && (
      <Confirm
        danger={ask.last}
        action={ask.action}
        cli={ask.cli}
        pending={update.isPending}
        error={update.error}
        onConfirm={() => apply(ask.patch)}
        onCancel={() => {
          update.reset();
          setAsk(null);
        }}
      >
        {ask.text}
      </Confirm>
    ),
    error: ask ? null : update.error,
  };
}
