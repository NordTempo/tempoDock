import { useApplyChanges, useDiscardChanges } from "../../data/mutations";
import { useChanges, useEnvironment } from "../../data/queries";
import type { EnvRef, PendingChanges } from "../../data/types";
import { Button, ErrorText } from "../../ui/kit";

/** Services that get a release when the changes are applied. */
function affected(changes: PendingChanges, running: string[]) {
  if (changes.variables.some((v) => v.scope === "environment")) return running;
  const named = new Set([
    ...changes.variables.flatMap((v) => (v.service ? [v.service] : [])),
    ...changes.settings.map((s) => s.service),
    ...changes.volumes.map((v) => v.service),
  ]);
  return running.filter((s) => named.has(s));
}

/**
 * "Apply N changes / Discard" for an environment's staged changes. Applying
 * is all or nothing, like the API: every staged change in the environment
 * goes live together.
 */
export function ApplyBar({ env }: { env: EnvRef }) {
  const changes = useChanges(env);
  const { services } = useEnvironment(env);
  const apply = useApplyChanges();
  const discard = useDiscardChanges();
  const busy = apply.isPending || discard.isPending;

  if (changes.count === 0) {
    const applied = apply.data;
    if (!applied) return null;
    const releases = applied.releases ?? [];
    const skipped = applied.skipped ?? [];
    return (
      <p className="py-2.5 text-dim">
        Applied.{" "}
        {releases.length > 0 &&
          `Started ${releases.map((r) => `${r.service} r${r.number}`).join(", ")}. `}
        {skipped.length > 0 &&
          `${skipped.join(", ")} picks it up on the next deploy (one is in progress).`}
      </p>
    );
  }

  const running = services
    .filter((s) => s.service.active_release)
    .map((s) => s.service.name);
  const releases = affected(changes, running);
  const n = changes.count;

  return (
    <div className="flex items-center gap-3 py-2.5">
      <Button primary disabled={busy} onClick={() => apply.mutate(env)}>
        Apply {n} {n === 1 ? "change" : "changes"}
      </Button>
      <Button disabled={busy} onClick={() => discard.mutate({ env })}>
        Discard all
      </Button>
      <span className="text-dim">
        {releases.length > 0
          ? `Creates a release for ${releases.join(", ")} in ${env.environment}. Health-checked before traffic.`
          : `Nothing running in ${env.environment} is redeployed; new deploys pick it up.`}
      </span>
      <ErrorText error={apply.error ?? discard.error} />
    </div>
  );
}
