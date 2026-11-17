import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { useState } from "react";
import { useApplyChanges, useDiscardChanges } from "../../data/mutations";
import {
  useChanges,
  useDeployBlock,
  useEnvironment,
  useRevealedChanges,
} from "../../data/queries";
import { typedValue } from "../../data/typed";
import type {
  DiscardSelection,
  EnvRef,
  Release,
  ServiceView,
  Variable,
  VariableChange,
} from "../../data/types";
import { isModEnter, modKey, useKeydown, useShortcuts } from "../../shell/keys";
import { useOrg } from "../../shell/location";
import { Dialog } from "../../ui/Dialog";
import { plural } from "../../ui/format";
import { Act, ErrorText, Glyph } from "../../ui/kit";
import { notify } from "../../ui/notice";
import { failurePhrase, stepPhrase } from "../../ui/status";
import { openHelp } from "../help/open";
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
 * What applying an environment's staged changes does, and the mutations:
 * one state for the service header's control, its review drawer and the
 * shared variables' panel. Applying is all or nothing, like the API.
 */
function useStaged({
  env,
  shows = () => false,
  variables = [],
  service,
}: {
  env: EnvRef;
  /**
   * Which staged variables show old and new values: those the table
   * above shows (admin, on request). The rest stay masked, as there.
   */
  shows?: (change: VariableChange) => boolean;
  /** All variables of the environment, to name overriding services. */
  variables?: Variable[];
  /** The service on screen: its own changes can be discarded alone. */
  service?: string | null;
}) {
  const changes = useChanges(env);
  const revealed = useRevealedChanges(
    env,
    changes.variables.some((c) => shows(c)),
  ).data;
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
          // Applying never retries the image setting (a failed new tag);
          // say what does, so a fixed setting isn't taken for a fixed deploy.
          `${s.service.name}'s new release runs ${shortImage(serving)}, the image serving now. To try ${shortImage(setting)} again with these changes, deploy instead.`,
        ]
      : [];
  });
  // A service with a volume stops its old container before the new one
  // starts: said as the rollback confirm says it.
  const stopping = affected.filter(
    (s) =>
      s.service.stateful && s.service.active_release && !s.service.stopped_at,
  );
  const downtimes = stopping.map(
    (s) =>
      `${s.service.name} is down for a few seconds while r${s.service.active_release} stops and the new release starts: only one copy can use its volume at a time.`,
  );
  // Releases can't start on an unreachable server.
  const blocked = useDeployBlock(env);
  const applyBlocked = releases.length > 0 ? blocked : undefined;

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
      const v = shows(c)
        ? values.get(`${c.service ?? ""}:${c.key}`)
        : undefined;
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
            ) : c.action === "add" || c.action === "update" ? (
              <span className="text-ok">
                {v?.new ?? typedValue(env, c.service, c.key) ?? "••••"}
              </span>
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
  return {
    env,
    service,
    services,
    changes,
    apply,
    discard,
    busy,
    releases,
    keptImages,
    downtimes,
    /** The services applying stops for a few seconds, by name. */
    stopping: stopping.map((s) => s.service.name),
    applyBlocked,
    own,
    ownCount,
    lines,
    applyLabel:
      releases.length > 0
        ? `Apply: release ${releases.join(", ")}`
        : "Apply, no release needed",
  };
}
type Staged = ReturnType<typeof useStaged>;

/**
 * Shared variables' staged changes (no service header there): the review
 * as a panel on the page. ⌘↵ applies.
 */
export function ApplyBox(props: Parameters<typeof useStaged>[0]) {
  const staged = useStaged(props);
  const { changes, busy, applyBlocked, apply, env } = staged;
  useKeydown((event) => {
    if (changes.count === 0 || busy || applyBlocked) return;
    if (isModEnter(event)) {
      event.preventDefault();
      apply.mutate(env);
    }
  });
  if (changes.count === 0 && !apply.data) return null;
  return (
    <section
      aria-labelledby="staged"
      className={
        changes.count > 0
          ? "flex flex-col gap-3 rounded-panel border border-warn-edge bg-stage-bg px-4 py-3"
          : ""
      }
    >
      <StagedReview staged={staged} />
    </section>
  );
}

/**
 * The service header's control while changes wait to apply: the count,
 * which opens the review drawer (A), and Apply (⌘↵), the view's primary.
 * It sits in the header's action row, so nothing below it moves. Where
 * applying stops a service for a few seconds, Apply opens the review
 * first, which says so; Apply there runs it.
 */
export function ApplyControl({
  env,
  service,
}: {
  env: EnvRef;
  service: string;
}) {
  const staged = useStaged({ env, service });
  const {
    changes,
    busy,
    applyBlocked,
    apply,
    downtimes,
    stopping,
    applyLabel,
  } = staged;
  const [open, setOpen] = useState(false);
  const run = () => {
    if (downtimes.length > 0 && !open) return setOpen(true);
    apply.mutate(env, {
      // The header's state follows the releases; this says it started.
      onSuccess: (r) =>
        notify(
          r.releases?.length
            ? `Applied: ${r.releases.map((x) => `${x.service} r${x.number}`).join(", ")} going live`
            : "Applied",
        ),
    });
  };
  useKeydown((event) => {
    if (changes.count === 0 || busy || applyBlocked) return;
    if (open) return; // the drawer's own Apply has it
    if (isModEnter(event)) {
      event.preventDefault();
      run();
    }
  });
  useShortcuts(
    changes.count > 0
      ? [{ key: "a", label: "review changes", run: () => setOpen(true) }]
      : [],
  );
  if (changes.count === 0 && !open) return null;
  return (
    <>
      {changes.count > 0 && (
        <span className="flex items-center gap-1.5 rounded-ctl border border-warn-edge bg-stage-bg py-0.5 pr-0.5 pl-1">
          <button
            type="button"
            className="inline-flex min-h-[24px] items-center gap-1.5 rounded-ctl px-1.5 text-xs text-warn-text hover:text-fg"
            title="Review changes waiting to apply (A)"
            aria-haspopup="dialog"
            onClick={() => setOpen(true)}
          >
            <span className="text-warn">
              <span className="dot" />
            </span>
            {/* Says what waits: "2 to apply", and which service applying
                stops for a moment ("▲ restarts web"), short so the header's
                action row doesn't wrap; the title says how long. */}
            {changes.count} to apply
            {stopping.length > 0 && (
              <span className="text-warn" title={downtimes.join(" ")}>
                ▲ restarts{" "}
                {stopping.length === 1
                  ? stopping[0]
                  : `${stopping.length} services`}
              </span>
            )}
          </button>
          <Act
            primary
            small
            cli={`dock apply -e ${env.environment}`}
            disabled={busy || !!applyBlocked}
            title={applyBlocked ?? applyLabel}
            onClick={run}
          >
            Apply <kbd>{modKey.trim()}↵</kbd>
          </Act>
        </span>
      )}
      {open && (
        <Dialog
          title="Changes waiting to apply"
          bare
          onClose={() => setOpen(false)}
          className="fixed inset-y-0 right-0 left-auto m-0 flex h-full max-h-none max-w-[440px] flex-col gap-3 overflow-y-auto rounded-none border-y-0 border-r-0 px-5 py-4"
        >
          <StagedReview staged={staged} drawer onClose={() => setOpen(false)} />
        </Dialog>
      )}
    </>
  );
}

/**
 * The staged changes as a list: what applying does, then Apply, Discard
 * and, on a service, Discard its own; after applying, each release as it
 * goes. In the drawer, ⌘↵ applies and Esc closes.
 */
function StagedReview({
  staged,
  drawer,
  onClose,
}: {
  staged: Staged;
  drawer?: boolean;
  onClose?: () => void;
}) {
  const {
    env,
    service,
    services,
    changes,
    apply,
    discard,
    busy,
    keptImages,
    downtimes,
    applyBlocked,
    own,
    ownCount,
    lines,
    applyLabel,
  } = staged;
  const applyNow = () => apply.mutate(env);
  // In the drawer, ⌘↵ applies (the header's control stands aside while
  // it is open; the panel has its own).
  useKeydown((event) => {
    if (!drawer || changes.count === 0 || busy || applyBlocked) return;
    if (isModEnter(event)) {
      event.preventDefault();
      applyNow();
    }
  });

  if (changes.count === 0) {
    const applied = apply.data;
    if (!applied) return null;
    const skipped = applied.skipped ?? [];
    const failed = applied.failed ?? [];
    return (
      <div className="flex flex-col gap-1 text-mut">
        <p>
          <span className="text-ok">✓</span> Applied.{" "}
          {skipped.length > 0 &&
            `${skipped.join(", ")} picks it up after the deploy in progress.`}
        </p>
        {(applied.releases ?? []).map((r) => (
          <AppliedRelease
            key={r.id}
            release={r}
            view={services.find((s) => s.service.name === r.service)}
          />
        ))}
        {failed.map((f) => (
          <p key={f} className="text-warn">
            ▲ Still waiting to apply: {f}
          </p>
        ))}
        {drawer && (
          <div className="mt-2">
            <button type="button" className="btn" onClick={onClose}>
              Close <kbd>esc</kbd>
            </button>
          </div>
        )}
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
        <h2 id="staged" className="font-semibold">
          <span className="text-warn">
            <span className="dot" />
          </span>{" "}
          {plural(changes.count, "change")} waiting to apply
        </h2>
        {drawer && (
          <button
            type="button"
            aria-label="esc, close"
            className="ml-auto inline-flex min-h-[24px] items-center rounded-ctl px-1 text-xs text-mut hover:text-fg"
            onClick={onClose}
          >
            <kbd>esc</kbd>
          </button>
        )}
      </div>
      <p className="text-xs text-mut">
        Nothing changes until you apply. Each affected service gets a new
        release, health checked as usual.{" "}
        <button
          type="button"
          className="underline underline-offset-3"
          onClick={() => openHelp("services")}
        >
          What's this?
        </button>
      </p>
      {changes.last_failure && (
        <p className="text-fail">
          The last apply failed, so its changes wait to apply again:{" "}
          {changes.last_failure}
        </p>
      )}
      {/* Long values wrap: nothing to scroll sideways, so the whole change
          is readable (and reachable without a mouse). */}
      <div className="font-mono text-xs leading-[1.9]">
        {lines.map((line) => (
          <div key={line.id} className="wrap-break-word whitespace-pre-wrap">
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
      {downtimes.map((text) => (
        <p key={text} className="text-xs text-warn-text">
          ▲ {text}
        </p>
      ))}
      <div className="flex flex-wrap items-start gap-2">
        <Act
          primary
          cli={`dock apply -e ${env.environment}`}
          disabled={busy || !!applyBlocked}
          title={applyBlocked}
          onClick={applyNow}
          data-autofocus={drawer || undefined}
        >
          {applyLabel} <kbd>{modKey.trim()}↵</kbd>
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
    </div>
  );
}

/**
 * A release an apply started, as it goes, from the live service view:
 * "web r11 is serving", or in the fail colour "web r11 failed its health
 * check · Details". The release number links to its page.
 */
function AppliedRelease({
  release: r,
  view,
}: {
  release: Release;
  view: ServiceView | undefined;
}) {
  const org = useOrg();
  const link = (text: ReactNode, className = "") => (
    <Link
      to="/$org/$project/$environment/$service/history/$number"
      params={{
        org,
        project: r.project,
        environment: r.environment,
        service: r.service,
        number: String(r.number),
      }}
      className={`underline decoration-faint underline-offset-2 hover:decoration-current ${className}`}
    >
      {text}
    </Link>
  );
  const name = (
    <>
      {r.service} {link(<span className="font-mono">r{r.number}</span>)}
    </>
  );
  const latest = view?.latest_release;
  if (view?.service.active_release === r.number) {
    // Traffic moved, but it serves once its health check passes.
    return view.service.status.health === "starting" ? (
      <p>
        <span className="text-prog">
          <Glyph char="◐" />
        </span>{" "}
        {name} is starting: its health check hasn't passed yet
      </p>
    ) : (
      <p className="text-fg">
        <span className="text-ok">●</span> {name} is serving
      </p>
    );
  }
  if (latest?.number === r.number && latest.status === "failed") {
    return (
      <p className="text-fail">
        ✕ {name} {failurePhrase(latest)} · {link("Details")}
      </p>
    );
  }
  if (view?.active_release && view.active_release.number > r.number) {
    return (
      <p>
        {name} was replaced by r{view.active_release.number}
      </p>
    );
  }
  const step =
    view?.deploying?.number === r.number ? view.deploying.status : r.status;
  return (
    <p>
      <span className="text-prog">
        <Glyph char="◐" />
      </span>{" "}
      {name} {stepPhrase[step]}
    </p>
  );
}
