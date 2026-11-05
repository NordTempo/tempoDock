import type { ReactNode } from "react";
import { useApplyChanges, useDiscardChanges } from "../../data/mutations";
import {
  useChanges,
  useDeployBlock,
  useEnvironment,
  useRevealedChanges,
} from "../../data/queries";
import type {
  DiscardSelection,
  EnvRef,
  Variable,
  VariableChange,
} from "../../data/types";
import { modKey, useKeydown } from "../../shell/keys";
import { plural } from "../../ui/format";
import { Act, ErrorText } from "../../ui/kit";
import { imageLabel, shortImage } from "../service/releases";
import {
  formatSetting,
  isSettingKey,
  settingLabel,
} from "../settings/settings";
import { affectedServices, nextRelease, stagedDomain } from "./apply";

const marks: Record<VariableChange["action"], string> = {
  add: "+",
  update: "~",
  remove: "−",
};

/**
 * An environment's staged changes, what applying them does, and the
 * buttons. Applying is all or nothing, like the API. ⌘↵ applies. With
 * `sticky` it stays in view above the command line while anything is
 * staged, so the way to apply is never scrolled away.
 */
export function ApplyBox({
  env,
  reveal,
  variables = [],
  service,
  sticky,
}: {
  env: EnvRef;
  /** Show old and new values (admin, on request). */
  reveal: boolean;
  /** All variables of the environment, to name overriding services. */
  variables?: Variable[];
  /** The service on screen: its own changes can be discarded alone. */
  service?: string | null;
  sticky?: boolean;
}) {
  const changes = useChanges(env);
  const revealed = useRevealedChanges(env, reveal).data;
  const { services } = useEnvironment(env);
  const apply = useApplyChanges();
  const discard = useDiscardChanges();
  const busy = apply.isPending || discard.isPending;
  const affected = affectedServices(changes, services);
  const releases = affected.map(nextRelease);
  // A config release keeps the serving image; say so where the image
  // setting differs (after a failed deploy of a new tag, or a rollback)
  // and the image itself isn't staged.
  const keptImages = affected.flatMap((s) => {
    const serving = s.active_release && imageLabel(s.active_release);
    const setting = s.service.image.replace(/^docker\.io\/(library\/)?/, "");
    const staged = changes.settings.some(
      (c) => c.service === s.service.name && c.field === "image",
    );
    return serving && setting && serving !== setting && !staged
      ? [
          `${s.service.name} keeps serving ${shortImage(serving)}; ${shortImage(setting)} waits for the next deploy.`,
        ]
      : [];
  });
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
    const failed = applied.failed ?? [];
    return (
      <div className="flex flex-col gap-1 text-mut">
        <p>
          <span className="text-ok">✓</span> Applied.{" "}
          {started.length > 0 && `Started ${started.join(", ")}. `}
          {skipped.length > 0 &&
            `${skipped.join(", ")} picks it up after the deploy in progress.`}
        </p>
        {failed.map((f) => (
          <p key={f} className="text-warn">
            ▲ Still staged: {f}
          </p>
        ))}
      </div>
    );
  }

  // The service's own changes, as `dock discard -s` selects them.
  const own: DiscardSelection = {
    variables: changes.variables
      .filter((c) => c.scope === "service" && c.service === service)
      .map((c) => ({ key: c.key, service: c.service })),
    settings: changes.settings
      .filter((c) => c.service === service)
      .map((c) => ({ service: c.service, field: c.field })),
    volumes: changes.volumes
      .filter((c) => c.service === service)
      .map((c) => ({ service: c.service, name: c.name })),
    services: changes.services
      .filter((c) => c.service === service)
      .map((c) => c.service),
    domains: changes.domains
      .filter((c) => c.service === service)
      .map((c) => ({ service: c.service, hostname: c.hostname })),
  };
  const ownCount =
    (own.variables?.length ?? 0) +
    (own.settings?.length ?? 0) +
    (own.volumes?.length ?? 0) +
    (own.services?.length ?? 0) +
    (own.domains?.length ?? 0);

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
    ...changes.services.map((c) => ({
      id: `c:${c.service}`,
      mark: marks[c.action],
      scope: c.service,
      body: (
        <>
          service{" "}
          <span className="text-mut">
            {c.action === "add"
              ? "(created when applied)"
              : "(archived when applied)"}
          </span>
        </>
      ),
      note: c.summary,
    })),
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
    // Generated domains to add have no hostname yet, so the index keeps keys apart.
    ...changes.domains.map((d, i) => ({
      id: `d:${i}:${d.service}:${d.hostname ?? ""}`,
      mark: marks[d.action],
      scope: d.service,
      body: stagedDomain(d),
    })),
  ];

  return (
    <section
      aria-labelledby="staged"
      className={`flex flex-col gap-3 rounded-panel border border-warn-edge bg-stage-bg px-4 py-3.5 ${sticky ? "sticky bottom-[64px] z-10 shadow-lg shadow-black/60" : ""}`}
    >
      <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
        <h2 id="staged" className="font-semibold">
          <span className="text-warn">
            <span className="dot" />
          </span>{" "}
          {plural(changes.count, "change")} staged
        </h2>
        <span className="text-xs text-mut">
          · nothing changes until you apply. Each affected service gets a new
          release, health checked as usual.
        </span>
      </div>
      {changes.last_failure && (
        <p className="text-fail">
          The last apply failed, so its changes are staged again:{" "}
          {changes.last_failure}
        </p>
      )}
      <div
        className={`overflow-x-auto font-mono text-xs leading-[1.9] ${sticky ? "max-h-[30vh] overflow-y-auto" : ""}`}
      >
        {lines.map((line) => (
          <div key={line.id} className="whitespace-nowrap">
            <span className="text-warn">{line.mark}</span>{" "}
            <span className="inline-block w-[9ch] text-faint">
              {line.scope}
            </span>
            {line.body}
            {line.note && <span className="text-mut"> {line.note}</span>}
          </div>
        ))}
      </div>
      {keptImages.map((text) => (
        <p key={text} className="text-xs text-mut">
          {text}
        </p>
      ))}
      <div className="flex flex-wrap items-start gap-2">
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
        {service && ownCount > 0 && ownCount < changes.count && (
          <Act
            cli={`dock discard -s ${service} -e ${env.environment}`}
            disabled={busy}
            onClick={() => discard.mutate({ env, selection: own })}
          >
            Discard {service}'s {ownCount}
          </Act>
        )}
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
