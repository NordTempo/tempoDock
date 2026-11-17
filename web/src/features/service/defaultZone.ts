import type { Domain, ServiceRef } from "../../data/types";

/**
 * How well a zone's name fits a project's or service's: 0 when its label
 * (klipit of klipit.no) is the project's name, 1 the service's, 2 when one
 * holds the other in a part of 3 characters or more (project detoo-web,
 * zone detoo.no), 3 when nothing ties them.
 */
function nameFit(zone: string, ref: ServiceRef) {
  const label = zone.split(".")[0] ?? "";
  const project = ref.project.toLowerCase();
  const service = ref.service.toLowerCase();
  if (label === project) return 0;
  if (label === service) return 1;
  const shares = (name: string) =>
    (label.length >= 3 && name.includes(label)) ||
    (name.length >= 3 && label.includes(name));
  return shares(project) || shares(service) ? 2 : 3;
}

/**
 * The zone "From Cloudflare" starts on when adding a domain to a service.
 * A zone the project's own hostnames already use comes first: this
 * service's, then its namesakes' in other environments, then any other
 * service's. Next a zone named after the project, after the service, or
 * sharing a part of its name with either. Each rule breaks the ties of
 * the one before, and the alphabet the rest. `domains` are the project's
 * (its overview's); generated ones say nothing about a zone.
 */
export function defaultZone(
  zones: readonly string[],
  ref: ServiceRef,
  domains: readonly Pick<
    Domain,
    "hostname" | "environment" | "service" | "generated"
  >[],
) {
  const own = domains.filter((d) => !d.generated);
  const usedBy = (zone: string) => {
    const on = own.filter(
      (d) => d.hostname === zone || d.hostname.endsWith(`.${zone}`),
    );
    if (on.length === 0) return 3;
    if (!on.some((d) => d.service === ref.service)) return 2;
    return on.some(
      (d) => d.service === ref.service && d.environment === ref.environment,
    )
      ? 0
      : 1;
  };
  const ranked = zones
    .map((zone) => ({
      zone,
      rank: usedBy(zone.toLowerCase()) * 4 + nameFit(zone.toLowerCase(), ref),
    }))
    .sort((a, b) => a.rank - b.rank || a.zone.localeCompare(b.zone));
  return ranked[0]?.zone ?? "";
}
