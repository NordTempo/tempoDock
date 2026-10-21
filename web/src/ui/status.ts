import type {
  Domain,
  Release,
  ReleaseSummary,
  Server,
  ServiceView,
} from "../data/types";

/** Status colors from the design: green, red, yellow, blue, gray. */
export type Tone = "ok" | "bad" | "warn" | "run" | "off";

type Status = { tone: Tone; label: string };

/** "out of memory (limit 512 MB)", for a container the kernel killed. */
export const outOfMemory = (s: ServiceView["service"]["status"]) =>
  s.memory_limit_mb
    ? `out of memory (limit ${s.memory_limit_mb} MB)`
    : "out of memory";

export function serviceStatus(s: ServiceView): Status {
  if (s.deploying) {
    return { tone: "run", label: `deploying r${s.deploying.number}` };
  }
  const { status, active_release } = s.service;
  switch (status.state) {
    case "running":
      return { tone: "ok", label: "running" };
    case "restarting":
      return { tone: "bad", label: `restarting (${status.restart_count})` };
    case "crashed":
      return {
        tone: "bad",
        label: status.oom_killed
          ? outOfMemory(status)
          : `crashed, exit ${status.exit_code}`,
      };
    // With an active release, no running container is a failure.
    case "stopped":
      return { tone: active_release ? "bad" : "off", label: "stopped" };
    case "none":
      return active_release
        ? { tone: "bad", label: "no container" }
        : { tone: "off", label: "not deployed" };
  }
}

type ReleaseState = Release["status"];

const releaseLabels: Record<ReleaseState, Status> = {
  queued: { tone: "run", label: "queued" },
  building: { tone: "run", label: "building" },
  starting: { tone: "run", label: "starting" },
  health_checking: { tone: "run", label: "health check" },
  active: { tone: "ok", label: "active" },
  failed: { tone: "bad", label: "failed" },
  superseded: { tone: "off", label: "superseded" },
};

export const releaseStatus = (s: ReleaseState) => releaseLabels[s];

/** True while a release is moving through the deploy steps. */
export const inFlight = (s: ReleaseState) =>
  s !== "active" && s !== "failed" && s !== "superseded";

const domainLabels: Record<Domain["status"], Status> = {
  added: { tone: "run", label: "checking DNS" },
  waiting_for_dns: { tone: "warn", label: "waiting for DNS" },
  issuing_certificate: { tone: "run", label: "issuing certificate" },
  active: { tone: "ok", label: "active" },
  error: { tone: "bad", label: "error" },
};

export const domainStatus = (d: Domain) => domainLabels[d.status];

/** Server status with its role, the one thing worth showing next to it. */
export function serverStatus(s: Server): Status {
  if (s.status === "offline") return { tone: "bad", label: "offline" };
  return { tone: "ok", label: s.role === "production" ? "prod" : "non-prod" };
}

/** One-line description of what a release deployed. */
export function changeText(r: Release | ReleaseSummary) {
  const from =
    "from_release" in r && r.from_release ? ` r${r.from_release}` : "";
  switch (r.source) {
    case "upload":
      return `source upload ${shortId(r.image_id)}`;
    case "image":
      return r.image_ref;
    case "config":
      return "config change";
    case "rollback":
      return `rollback to${from || " earlier release"}`;
    case "restart":
      return `restart${from && ` of${from}`}`;
    case "promote":
      return `promote ${r.image_ref}`;
  }
}

/** "sha256:4be1c0ffee…" as "4be1c0ffee12". */
export const shortId = (id: string) => id.replace(/^sha256:/, "").slice(0, 12);
