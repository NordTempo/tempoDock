import type {
  Domain,
  Release,
  ReleaseSummary,
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
  const { status, active_release } = s.service;
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

export const domainState = (d: Domain) => domainStates[d.status];

/** "sha256:4be1c0ffee…" as "4be1c0f". */
export const shortId = (id: string) => id.replace(/^sha256:/, "").slice(0, 7);

/** What a release deployed, in a few words: "deploy nginx:alpine". */
export function releaseWhat(r: Release | ReleaseSummary) {
  const from =
    "from_release" in r && r.from_release ? ` r${r.from_release}` : "";
  switch (r.source) {
    case "upload":
      return "deploy from source";
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

/** Which environment kinds a server accepts, as shown in tables. */
export function serverKinds(s: {
  accepts_production: boolean;
  accepts_non_production: boolean;
}) {
  const kinds = [
    s.accepts_production && "production",
    s.accepts_non_production && "non-production",
  ].filter(Boolean);
  return kinds.length ? kinds.join(", ") : "nothing";
}
