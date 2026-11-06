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

const acceptsField = {
  production: "accepts_production",
  non_production: "accepts_non_production",
} as const satisfies Record<EnvironmentKind, keyof Server>;

/**
 * An admin's switches for one server: which kinds it accepts, whether it
 * takes new placements and whether it builds images. Every change asks
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
        reason={
          lastProduction
            ? "the only server for production; let another take it first"
            : undefined
        }
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
          reason={
            lastPlacement
              ? "the only server taking new projects; open another first"
              : undefined
          }
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
        <Check
          label="builds images"
          checked={server.builder}
          disabled={update.isPending}
          onChange={(on) => {
            if (on)
              return setAsk({
                patch: { builder: true },
                action: "Build images here",
                cli: `dock server update ${name} --builder`,
                text: (
                  <>
                    Build images on{" "}
                    <b className="font-mono font-medium">{name}</b>?{" "}
                    <span className="text-mut">
                      New builds may run here and use its CPU and memory.
                    </span>
                  </>
                ),
              });
            const others = servers.filter((s) => s.name !== name && s.builder);
            setAsk({
              patch: { builder: false },
              action: "Stop building",
              cli: `dock server update ${name} --no-builder`,
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
                </>
              ),
              last: others.length === 0,
            });
          }}
        />
        {saved("builder")}
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
