import { queryOptions } from "@tanstack/react-query";
import { api, body, done, list } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";

// The organization's uploaded TLS certificates (ADR 0112): Settings ›
// Certificates. Domains show the one they are served with on their own
// rows (Domain.certificate), from the project overview.

type APICertificate = components["schemas"]["Certificate"];

/** A certificate with its lists as arrays (the API's may be null). */
export type Certificate = APICertificate & {
  names: string[];
  domains: NonNullable<APICertificate["domains"]>;
};

const toCertificate = (c: APICertificate): Certificate => ({
  ...c,
  names: c.names ?? [],
  domains: c.domains ?? [],
});
export type AddCertificateRequest =
  components["schemas"]["AddCertificateRequest"];

/** live.ts refetches it, and the overviews, on certificate.* audit events. */
export const certificatesKey = ["org", "certificates"];

export const certificateQueries = {
  list: () =>
    queryOptions({
      queryKey: certificatesKey,
      queryFn: async () =>
        (await list(api.GET("/api/v1/org/certificates"))).map(toCertificate),
    }),
};

/**
 * What an upload or removal changes here; the overviews that show each
 * domain's certificate refetch from its audit event (live.ts).
 */
const touched = () => [certificatesKey, ["audit"]];

export const useAddCertificate = () =>
  useAction(
    (req: AddCertificateRequest) =>
      body(api.POST("/api/v1/org/certificates", { body: req })),
    touched,
  );

export const useRemoveCertificate = () =>
  useAction(
    (id: number) =>
      done(
        api.DELETE("/api/v1/org/certificates/{id}", {
          params: { path: { id } },
        }),
      ),
    touched,
  );

/** Days from now until iso, rounded down; negative once it has passed. */
export function daysLeft(iso: string, now = Date.now()) {
  return Math.floor((new Date(iso).getTime() - now) / 86_400_000);
}
