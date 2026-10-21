import type {
  Actor,
  DeployStep,
  DomainStatus,
  ReleaseChange,
  ReleaseStatus,
  Server,
  ServiceState,
} from "../data/types";

/** Status colors from the design: green, red, yellow, blue, gray. */
export type Tone = "ok" | "bad" | "warn" | "run" | "off";

type Status = { tone: Tone; label: string };

export function serviceStatus(s: ServiceState): Status {
  switch (s.state) {
    case "active":
      return { tone: "ok", label: "active" };
    case "deploying":
      return { tone: "run", label: `deploying r${s.release}` };
    case "crash_loop":
      return { tone: "bad", label: "crash loop" };
    case "stopped":
      return { tone: "off", label: "stopped" };
  }
}

export const stepLabel = (name: DeployStep["name"]) => name.replace("_", " ");

export function releaseStatus(s: ReleaseStatus): Status {
  switch (s.state) {
    case "in_progress": {
      const running = s.steps.find((step) => step.state === "running");
      return {
        tone: "run",
        label: running ? stepLabel(running.name) : "queued",
      };
    }
    case "active":
      return { tone: "ok", label: "active" };
    case "failed":
      return { tone: "bad", label: "failed" };
    case "superseded":
      return { tone: "off", label: "superseded" };
  }
}

export function domainStatus(s: DomainStatus): Status {
  switch (s.state) {
    case "active":
      return { tone: "ok", label: "active" };
    case "waiting_dns":
      return { tone: "warn", label: "waiting for DNS" };
    case "issuing_certificate":
      return { tone: "run", label: "issuing certificate" };
    case "error":
      return { tone: "bad", label: "error" };
  }
}

export const diskFraction = (s: Server) => s.disk.usedGb / s.disk.totalGb;

/** Server status with the one number worth showing next to it. */
export function serverStatus(s: Server): Status {
  if (s.status.state === "offline") return { tone: "bad", label: "offline" };
  const disk = Math.round(diskFraction(s) * 100);
  if (disk >= 80) return { tone: "warn", label: `disk ${disk}%` };
  const role = s.role === "production" ? "prod" : "non-prod";
  return { tone: "ok", label: `${role} · ${s.cpuPercent}%` };
}

/** One-line description of what a release changed. */
export function changeText(c: ReleaseChange) {
  switch (c.kind) {
    case "commit":
      return `${c.sha} ${c.message}`;
    case "image":
      return c.ref;
    case "config":
      return `config: ${c.summary}`;
    case "rollback":
      return `rollback to r${c.to}`;
    case "redeploy":
      return `redeploy of r${c.of}`;
    case "promote":
      return `promote ${c.from} r${c.release}`;
  }
}

export function actorDetail(a: Actor) {
  return a.kind === "user"
    ? "user"
    : `token · ${a.scope} scope · ${a.limit || "all"}`;
}
