import { type ReactNode, useState } from "react";
import { useUpdateServer } from "../../data/mutations";
import { useServers } from "../../data/queries";
import type { EnvironmentKind, Server, ServerPatch } from "../../data/types";
import { Check, Confirm } from "../../ui/kit";
import { kindLabel } from "../../ui/status";

type Ask = { patch: ServerPatch; action: string; cli: string; text: ReactNode };

const acceptsField = {
  production: "accepts_production",
  non_production: "accepts_non_production",
} as const satisfies Record<EnvironmentKind, keyof Server>;

/**
 * An admin's switches for one server: which kinds it accepts and whether
 * it takes new placements. Turning one on applies at once; turning one off
 * asks first (the `confirm` node, which the caller places).
 */
export function useServerSwitches(server: Server) {
  const update = useUpdateServer();
  const servers = useServers();
  const [ask, setAsk] = useState<Ask | null>(null);
  const { name } = server;
  const apply = (patch: ServerPatch) =>
    update.mutate({ name, patch }, { onSuccess: () => setAsk(null) });

  const kindSwitch = (kind: EnvironmentKind) => {
    const field = acceptsField[kind];
    const words = kindLabel[kind];
    // Where new environments of this kind can still go.
    const others = servers.filter(
      (s) => s.name !== name && s[field] && !s.disabled_for_placement,
    );
    return (
      <Check
        label={words}
        checked={server[field]}
        disabled={update.isPending}
        onChange={(on) => {
          if (on) return apply({ [field]: true });
          setAsk({
            patch: { [field]: false },
            action: `Stop ${words}`,
            cli: `dock server update ${name} --no-${words}`,
            text: (
              <>
                Stop placing {words} environments on{" "}
                <b className="font-mono font-medium">{name}</b>?{" "}
                <span className="text-mut">
                  Environments already there stay.{" "}
                  {others.length
                    ? `New ones go to ${others.map((s) => s.name).join(", ")}.`
                    : `No other server takes new ${words} environments, so projects without one can't create one.`}
                </span>
              </>
            ),
          });
        }}
      />
    );
  };

  return {
    accepts: (
      <span className="inline-flex flex-wrap gap-x-3">
        {kindSwitch("production")}
        {kindSwitch("non_production")}
      </span>
    ),
    placement: (
      <Check
        label="new placements"
        checked={!server.disabled_for_placement}
        disabled={update.isPending}
        onChange={(on) => {
          if (on) return apply({ disabled_for_placement: false });
          setAsk({
            patch: { disabled_for_placement: true },
            action: "Disable placement",
            cli: `dock server update ${name} --disable-placement`,
            text: (
              <>
                Stop placing new projects on{" "}
                <b className="font-mono font-medium">{name}</b>?{" "}
                <span className="text-mut">
                  What is there stays, and its projects keep adding environments
                  here. Admins can still pin projects to it.
                </span>
              </>
            ),
          });
        }}
      />
    ),
    confirm: ask && (
      <Confirm
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
