import type {
  EnvironmentView,
  ReleaseSummary,
  ServiceView,
} from "../../data/types";

/** `dock promote staging production`, with --service when not all go. */
export function promoteCli(
  from: string,
  to: string,
  services?: readonly string[],
) {
  const only = services ? ` --service ${services.join(",")}` : "";
  return `dock promote ${from} ${to}${only}`;
}

/** Staging-like first, production last, so "from → to" reads as a promotion. */
export function defaultPair(envs: EnvironmentView[]) {
  const names = envs.map((e) => e.environment.name);
  const to =
    envs.find((e) => e.environment.kind === "production")?.environment.name ??
    names.at(-1);
  const from = names.find((n) => n !== to);
  return { from, to };
}

/**
 * Every non-production → production pair of a project, for the palette.
 * Preview environments (ADR 0052) are left out: up to 20 of them come and go.
 */
export const promotionPairs = (envs: EnvironmentView[]) =>
  envs
    .filter((e) => e.environment.kind === "production")
    .flatMap((to) =>
      envs
        .filter(
          (e) => e.environment.kind !== "production" && !e.environment.preview,
        )
        .map((from) => ({ from, to })),
    );

/** One service of a promotion: its release goes over, or why not. */
export type PlanRow = {
  service: string;
  /** The source's active release, which gets promoted. */
  release?: ReleaseSummary;
  /** What the target runs now. */
  current?: ReleaseSummary;
  /** Set when the service is left alone. */
  skip?: string;
};

/** A release's registry digest, when its history is loaded and has one. */
export type DigestOf = (
  environment: string,
  service: string,
  release: number,
) => string | undefined;

/**
 * What promoting `from` to `to` would do per service, from what both run
 * now: the source's active image goes over unless the target lacks the
 * service, already runs that image (same registry digest), or is
 * mid-deploy.
 */
export function promotionPlan(
  from: EnvironmentView,
  to: EnvironmentView,
  digestOf: DigestOf,
): PlanRow[] {
  const target = new Map<string, ServiceView>(
    to.services.map((s) => [s.service.name, s]),
  );
  const there = to.environment.name;
  return from.services.map((s): PlanRow => {
    const service = s.service.name;
    const release = s.active_release;
    const twin = target.get(service);
    const current = twin?.active_release;
    if (!release) {
      return { service, skip: `nothing active in ${from.environment.name}` };
    }
    if (!twin) return { service, release, skip: `not in ${there}` };
    if (twin.deploying) {
      return {
        service,
        release,
        current,
        skip: `r${twin.deploying.number} is deploying in ${there}`,
      };
    }
    const digest = digestOf(from.environment.name, service, release.number);
    if (
      current &&
      digest &&
      digest === digestOf(there, service, current.number)
    ) {
      return { service, release, current, skip: "already runs this image" };
    }
    return { service, release, current };
  });
}
