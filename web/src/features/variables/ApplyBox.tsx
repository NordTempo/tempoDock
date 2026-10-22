import type { ReactNode } from "react";
import { useApplyChanges, useDiscardChanges } from "../../data/mutations";
import {
  useChanges,
  useDeployBlock,
  useEnvironment,
  useRevealedChanges,
} from "../../data/queries";
import type { EnvRef, Variable, VariableChange } from "../../data/types";
import { modKey, useKeydown } from "../../shell/keys";
import { plural } from "../../ui/format";
import { Act, ErrorText } from "../../ui/kit";
import {
  formatSetting,
  isSettingKey,
  settingLabel,
} from "../settings/settings";
import { affectedServices, nextRelease } from "./apply";

const marks: Record<VariableChange["action"], string> = {
  add: "+",
  update: "~",
  remove: "−",
};

/**
 * An environment's staged changes, what applying them does, and the
 * buttons. Applying is all or nothing, like the API. ⌘↵ applies.
 */
export function ApplyBox({
  env,
  reveal,
  variables = [],
}: {
  env: EnvRef;
  /** Show old and new values (admin, on request). */
  reveal: boolean;
  /** All variables of the environment, to name overriding services. */
  variables?: Variable[];
}) {
  const changes = useChanges(env);
  const revealed = useRevealedChanges(env, reveal).data;
  const { services } = useEnvironment(env);
  const apply = useApplyChanges();
  const discard = useDiscardChanges();
  const busy = apply.isPending || discard.isPending;
  const releases = affectedServices(changes, services).map(nextRelease);
  // Releases can't start on an unreachable server.
  const blocked = useDeployBlock(env);
  const applyBlocked = releases.length > 0 ? blocked : undefined;

  useKeydown((event) => {
    if (changes.count === 0 || busy || applyBlocked) return;
    if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
      event.preventDefault();
      apply.mutate(env);
    }
  });

  if (changes.count === 0) {
    const applied = apply.data;
    if (!applied) return null;
    const started = (applied.releases ?? []).map(
      (r) => `${r.service} r${r.number}`,
    );
    const skipped = applied.skipped ?? [];
    return (
      <p className="text-mut">
        <span className="text-ok">✓</span> Applied.{" "}
        {started.length > 0 && `Started ${started.join(", ")}. `}
        {skipped.length > 0 &&
          `${skipped.join(", ")} picks it up after the deploy in progress.`}
      </p>
    );
  }

  const values = new Map(
    (revealed?.variables ?? []).map((c) => [`${c.service ?? ""}:${c.key}`, c]),
  );
  const lines: Array<{
    id: string;
    mark: string;
    scope: string;
    body: ReactNode;
    note?: string;
  }> = [
    ...changes.variables.map((c) => {
      const v = values.get(`${c.service ?? ""}:${c.key}`);
      const overriders =
        c.scope === "environment"
          ? variables
              .filter(
                (x) => x.scope === "service" && x.key === c.key && x.service,
              )
              .map((x) => x.service)
          : [];
      return {
        id: `v:${c.service ?? ""}:${c.key}`,
        mark: marks[c.action],
        scope: c.scope === "environment" ? "env" : (c.service ?? ""),
        body: (
          <>
            {c.key}{" "}
            {c.action === "update" && v ? (
              <>
                <span className="text-fail">{v.old}</span> →{" "}
                <span className="text-ok">{v.new}</span>
              </>
            ) : c.action === "add" ? (
              <span className="text-ok">{v?.new ?? "••••"}</span>
            ) : c.action === "remove" ? (
              <span className="text-fail line-through">{v?.old ?? "••••"}</span>
            ) : (
              <span className="text-mut">changed</span>
            )}
          </>
        ),
        note: overriders.length
          ? `${overriders.join(", ")} ${overriders.length === 1 ? "keeps its" : "keep their"} override`
          : undefined,
      };
    }),
    ...changes.settings.map((s) => ({
      id: `s:${s.service}:${s.field}`,
      mark: "~",
      scope: s.service,
      body: isSettingKey(s.field) ? (
        <>
          {settingLabel[s.field]}{" "}
          <span className="text-fail">{formatSetting(s.field, s.old)}</span> →{" "}
          <span className="text-ok">{formatSetting(s.field, s.new)}</span>
        </>
      ) : (
        <>
          {s.field} {s.old} → {s.new}
        </>
      ),
    })),
    ...changes.volumes.map((v) => ({
      id: `m:${v.service}:${v.name}`,
      mark: "+",
      scope: v.service,
      body: (
        <>
          volume {v.name} at {v.mount_path}
        </>
      ),
    })),
  ];

  return (
    <section
      aria-labelledby="staged"
      className="flex flex-col gap-3 border border-warn px-4 py-3.5"
    >
      <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1.5">
        <h2 id="staged" className="text-[15px] font-semibold">
          <span className="text-warn">▲</span> {plural(changes.count, "change")}{" "}
          not applied yet
        </h2>
        <span className="text-mut">
          Nothing changes until you apply. Each affected service gets a new
          release, health checked as usual.
        </span>
      </div>
      {changes.last_failure && (
        <p className="text-fail">
          The last apply failed, so its changes are staged again:{" "}
          {changes.last_failure}
        </p>
      )}
      <div className="overflow-x-auto font-mono text-xs leading-[1.9]">
        {lines.map((line) => (
          <div key={line.id} className="whitespace-nowrap">
            <span className="text-warn">{line.mark}</span>{" "}
            <span className="inline-block w-[9ch] text-mut">{line.scope}</span>
            {line.body}
            {line.note && <span className="text-mut"> {line.note}</span>}
          </div>
        ))}
      </div>
      <div className="flex flex-wrap items-start gap-2.5">
        <Act
          primary
          cli={`dock apply -e ${env.environment}`}
          disabled={busy || !!applyBlocked}
          title={applyBlocked}
          onClick={() => apply.mutate(env)}
        >
          {releases.length > 0
            ? `Apply: release ${releases.join(", ")}`
            : "Apply, no release needed"}{" "}
          <kbd>{modKey.trim()}↵</kbd>
        </Act>
        <Act
          cli={`dock discard -e ${env.environment}`}
          disabled={busy}
          onClick={() => discard.mutate({ env })}
        >
          Discard all
        </Act>
        <span className="self-center">
          {applyBlocked && (
            <span className="text-fail">Disabled: {applyBlocked}. </span>
          )}
          <ErrorText error={apply.error ?? discard.error} />
        </span>
      </div>
    </section>
  );
}
