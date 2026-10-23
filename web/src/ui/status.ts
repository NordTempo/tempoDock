import type {
  Domain,
  EnvironmentKind,
  Release,
  ReleaseSummary,
  ServerStatus,
  ServiceView,
} from "../data/types";

/** Status colors: green, yellow, red, blue, gray. */
export type Tone = "ok" | "warn" | "fail" | "prog" | "mut";

/**
 * Status glyphs, one meaning each: ● running, ◐ in progress, ▲ needs action,
 * ✕ failed, ↻ restarting, ○ stopped, ✓ done, – skipped. ◐ renders as a
 * static CSS half circle (see Glyph in ./kit).
 */
export type GlyphChar = "●" | "◐" | "▲" | "✕" | "↻" | "○" | "✓" | "–";

export type State = { tone: Tone; glyph?: GlyphChar; label: string };

/** Tailwind needs whole class names, so tones map to literal classes. */
export const toneText: Record<Tone, string> = {
  ok: "text-ok",
  warn: "text-warn",
  fail: "text-fail",
  prog: "text-prog",
  mut: "text-mut",
};

type ReleaseStatus = Release["status"];

const releaseStates: Record<ReleaseStatus, State> = {
  queued: { tone: "prog", glyph: "◐", label: "queued" },
  building: { tone: "prog", glyph: "◐", label: "building" },
  starting: { tone: "prog", glyph: "◐", label: "starting" },
  health_checking: { tone: "prog", glyph: "◐", label: "health checking" },
  active: { tone: "ok", glyph: "●", label: "active" },
  failed: { tone: "fail", glyph: "✕", label: "failed" },
  superseded: { tone: "mut", label: "superseded" },
};

export const releaseState = (s: ReleaseStatus) => releaseStates[s];

/** True while a release is moving through the deploy steps. */
export const inFlight = (s: ReleaseStatus) =>
  s !== "active" && s !== "failed" && s !== "superseded";

/** The deploy step a release in flight is on, as a phrase. */
export const stepPhrase: Record<ReleaseStatus, string> = {
  queued: "queued",
  building: "building the image",
  starting: "starting the container",
  health_checking: "health checking",
  active: "live",
  failed: "failed",
  superseded: "replaced",
};

/** "ran out of memory (limit 512 MB)" */
export const oomPhrase = (status: ServiceView["service"]["status"]) =>
  status.memory_limit_mb
    ? `ran out of memory (limit ${status.memory_limit_mb} MB)`
    : "ran out of memory";

/** How recent a restart must be for a running container to count as looping. */
const LOOP_WINDOW_MS = 10 * 60_000;

/**
 * A container that keeps restarting: Docker reports `restarting` only in
 * the moment between exit and start, so a `running` container that was
 * restarted within the last few minutes counts too.
 */
export function isCrashLooping(status: ServiceView["service"]["status"]) {
  if (status.state === "restarting") return true;
  return (
    status.state === "running" &&
    status.restart_count > 0 &&
    !!status.updated_at &&
    Date.now() - Date.parse(status.updated_at) < LOOP_WINDOW_MS
  );
}

/** A service's container state; a deploy in flight wins. */
export function serviceState(s: ServiceView): State {
  if (s.deploying) {
    return releaseStates[s.deploying.status];
  }
  return containerState(s.service);
}

/** A service's container state, without deploys (as on server pages). */
export function containerState({
  status,
  active_release,
}: Pick<ServiceView["service"], "status" | "active_release">): State {
  if (isCrashLooping(status)) {
    return {
      tone: "fail",
      glyph: "↻",
      label: `crash loop, ${status.restart_count}×, exit ${status.exit_code}`,
    };
  }
  switch (status.state) {
    case "running":
      return { tone: "ok", glyph: "●", label: "running" };
    case "restarting":
      return {
        tone: "fail",
        glyph: "↻",
        label: `crash loop, ${status.restart_count}×, exit ${status.exit_code}`,
      };
    case "crashed":
      return {
        tone: "fail",
        glyph: "✕",
        label: status.oom_killed
          ? oomPhrase(status)
          : `crashed, exit ${status.exit_code}`,
      };
    // With an active release, no running container is a failure.
    case "stopped":
      return {
        tone: active_release ? "fail" : "mut",
        glyph: "○",
        label: "stopped",
      };
    case "none":
      return active_release
        ? { tone: "fail", glyph: "✕", label: "no container" }
        : { tone: "mut", glyph: "○", label: "not deployed" };
  }
}

const domainStates: Record<Domain["status"], State> = {
  added: { tone: "prog", glyph: "◐", label: "checking DNS" },
  waiting_for_dns: { tone: "warn", glyph: "▲", label: "waiting for DNS" },
  issuing_certificate: {
    tone: "prog",
    glyph: "◐",
    label: "issuing certificate",
  },
  active: { tone: "ok", glyph: "●", label: "active" },
  error: { tone: "fail", glyph: "✕", label: "error" },
};

export const domainState = (d: Pick<Domain, "status">) =>
  domainStates[d.status];

/** "sha256:4be1c0ffee…" as "4be1c0f". */
export const shortId = (id: string) => id.replace(/^sha256:/, "").slice(0, 7);

/** What a release deployed, in a few words: "deploy nginx:alpine". */
export function releaseWhat(r: Release | ReleaseSummary) {
  const from =
    "from_release" in r && r.from_release ? ` r${r.from_release}` : "";
  switch (r.source) {
    case "upload":
      // From a push (ADR 0018): "deploy abc1234 fix checkout".
      return r.commit
        ? `deploy ${r.commit.sha.slice(0, 7)} ${r.commit.message}`.trim()
        : "deploy from source";
    case "image":
      return "deploy";
    case "config":
      return "config change";
    case "rollback":
      return `roll back to${from || " an earlier release"}`;
    case "restart":
      return "restart";
    case "promote":
      return "promote";
  }
}

/** Why a release failed, as the end of "r61 failed …". */
export function failurePhrase(r: { error_code?: string }) {
  switch (r.error_code) {
    case "health_check_failed":
      return "failed its health check";
    case "build_failed":
      return "failed to build";
    default:
      return "failed";
  }
}

/** An environment kind in words, as the CLI writes it. */
export const kindLabel: Record<EnvironmentKind, string> = {
  production: "production",
  non_production: "non-production",
};

const serverStates: Record<ServerStatus, State> = {
  online: { tone: "ok", glyph: "●", label: "online" },
  offline: { tone: "fail", glyph: "✕", label: "offline" },
  incompatible: { tone: "warn", glyph: "▲", label: "incompatible" },
  joining: { tone: "prog", glyph: "◐", label: "joining" },
  revoked: { tone: "mut", glyph: "○", label: "revoked" },
};

/** A server's link status: "● online", "◐ joining". */
export const serverState = (s: ServerStatus) => serverStates[s];

const semver = (v: string) =>
  /^v?(\d+)\.(\d+)\.(\d+)/.exec(v)?.slice(1).map(Number);

/**
 * Whether a server runs an older dock than the control plane: compared as
 * versions when both are, else any difference counts. Unknown is not
 * behind.
 */
export function versionBehind(version: string, controlPlane: string) {
  if (!version || !controlPlane || version === controlPlane) return false;
  const a = semver(version);
  const b = semver(controlPlane);
  if (!a || !b) return true;
  for (let i = 0; i < 3; i++) {
    const d = (a[i] ?? 0) - (b[i] ?? 0);
    if (d) return d < 0;
  }
  return false;
}
