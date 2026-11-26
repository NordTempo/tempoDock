import { useSuspenseQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useAddDomain } from "../../data/mutations";
import { queries } from "../../data/queries";
import type { Domain, ServiceRef } from "../../data/types";
import { Dialog } from "../../ui/Dialog";
import { Check } from "../../ui/kit";
import { useCloudflareZones } from "../cloudflare/data";
import { Footer, invalid, useValidation } from "../create/form";
import { defaultName, defaultZone, takenBy } from "./defaultZone";

/**
 * Adds a hostname: your own (you create its DNS record), or from a zone
 * connected to Dock, which Dock routes through the server's Cloudflare
 * tunnel (ADR 0036). The Cloudflare choice only shows once a zone is
 * connected. onAdded gets the new domain once it is added.
 */
export default function AddDomainDialog({
  serviceRef: ref,
  onClose,
  onAdded,
}: {
  serviceRef: ServiceRef;
  onClose: () => void;
  onAdded: (domain: Domain) => void;
}) {
  const add = useAddDomain();
  const zones = useCloudflareZones().data ?? [];
  const [mode, setMode] = useState<"own" | "cloudflare">("own");
  const [hostname, setHostname] = useState("");
  const [redirect, setRedirect] = useState("");
  // The project's hostnames and this environment, from the overview the
  // service's page is on.
  const { domains, environment } = useSuspenseQuery({
    ...queries.overview(ref.project),
    select: (o) => ({
      domains: o.environments.flatMap((e) =>
        e.services.flatMap((s) => s.domains),
      ),
      environment: o.environments.find(
        (e) => e.environment.name === ref.environment,
      )?.environment,
    }),
  }).data;
  // Starts as the environment's name (defaultName); what is typed stays.
  const [name, setName] = useState(() => defaultName(environment));
  const [zone, setZone] = useState("");
  // A hostname that collected analytics before carries its site's history
  // here unless this is set (ADR 0037).
  const [newSite, setNewSite] = useState(false);
  // Until one is chosen, the zone that fits the project (defaultZone).
  const picked =
    zone ||
    defaultZone(
      zones.map((z) => z.name),
      ref,
      domains,
    );
  // A full hostname typed into the name field keeps one zone.
  const sub = name
    .trim()
    .replace(/\.$/, "")
    .replace(new RegExp(`(^|\\.)${picked.replaceAll(".", "\\.")}$`), "");
  // Without a connected zone there is nothing to choose: the dialog is
  // just your own hostname, with no radio buttons or option box.
  const canCloudflare = zones.length > 0;
  const Own = canCloudflare ? "label" : "div";
  const fromCloudflare = canCloudflare && mode === "cloudflare";
  const host = fromCloudflare
    ? sub
      ? `${sub}.${picked}`
      : picked
    : hostname.trim();
  const target = fromCloudflare ? "" : redirect.trim();
  const flags = `-s ${ref.service} -e ${ref.environment}`;
  // A missing hostname marks its field on submit (the one validation pattern).
  const v = useValidation();
  // A hostname the project already has is said as typed; the server
  // answers for the ones outside it.
  const hostError =
    takenBy(host, domains) ?? v.required(!host, "Enter a hostname.");
  const option = (on: boolean) =>
    `flex gap-2.5 rounded-ctl border p-2.5 ${on ? "border-edge bg-sel" : "border-line"}`;
  return (
    <Dialog title={`Add a domain to ${ref.service}`} onClose={onClose} wide>
      <form
        ref={v.form}
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (!v.check(!!host && !hostError)) return;
          add.mutate(
            {
              ref,
              hostname: host,
              ...(target && { redirect_to: target }),
              ...(fromCloudflare && { cloudflare: true }),
              ...(newSite && !target && { analytics: "new" as const }),
            },
            {
              onSuccess: (added) => {
                onAdded(added.domain);
                onClose();
              },
            },
          );
        }}
      >
        <Own className={canCloudflare ? option(!fromCloudflare) : "flex"}>
          {canCloudflare && (
            <input
              type="radio"
              name="mode"
              checked={!fromCloudflare}
              onChange={() => setMode("own")}
            />
          )}
          <span className="flex min-w-0 flex-1 flex-col gap-2">
            <span>
              Your own hostname
              <span className="block text-mut">
                You point its DNS here; until it resolves, its row shows the
                record.
              </span>
            </span>
            {!fromCloudflare && (
              <span className="flex flex-wrap gap-2">
                <input
                  aria-label="Hostname"
                  aria-invalid={!!hostError}
                  data-autofocus
                  value={hostname}
                  onChange={(e) => setHostname(e.target.value)}
                  placeholder="app.example.com"
                  className={`field min-w-0 flex-[1_1_220px] font-mono ${invalid(hostError)}`}
                />
                <label className="flex min-w-0 basis-full items-center gap-2 text-mut">
                  Redirect to
                  <input
                    title="Permanently redirect to this hostname instead of serving the service"
                    value={redirect}
                    onChange={(e) => setRedirect(e.target.value)}
                    placeholder="optional, like example.com"
                    className="field min-w-0 flex-1 font-mono"
                  />
                </label>
                {hostError && (
                  <span role="alert" className="basis-full text-xs text-fail">
                    {hostError}
                  </span>
                )}
              </span>
            )}
          </span>
        </Own>
        {canCloudflare && (
          <label className={option(fromCloudflare)}>
            <input
              type="radio"
              name="mode"
              checked={fromCloudflare}
              onChange={() => setMode("cloudflare")}
            />
            <span className="flex min-w-0 flex-1 flex-col gap-2">
              <span>
                From Cloudflare
                <span className="block text-mut">
                  Dock creates the record and routes it through the server's
                  tunnel.
                </span>
              </span>
              {fromCloudflare && (
                <>
                  <span className="flex flex-wrap items-center gap-2">
                    <input
                      aria-label="Name"
                      aria-invalid={!!hostError}
                      data-autofocus
                      value={name}
                      onChange={(e) => setName(e.target.value)}
                      placeholder="shop, or empty for the zone"
                      className={`field min-w-0 flex-[1_1_180px] font-mono ${invalid(hostError)}`}
                    />
                    <span className="text-mut">.</span>
                    <select
                      aria-label="Zone"
                      value={picked}
                      onChange={(e) => setZone(e.target.value)}
                      className="field font-mono"
                    >
                      {zones.map((z) => (
                        <option key={z.name} value={z.name}>
                          {z.name}
                        </option>
                      ))}
                    </select>
                    {hostError && (
                      <span
                        role="alert"
                        className="basis-full text-xs text-fail"
                      >
                        {hostError}
                      </span>
                    )}
                  </span>
                  <span className="text-mut">
                    {zones.length} connected{" "}
                    {zones.length === 1 ? "zone" : "zones"}:{" "}
                    <span className="font-mono">
                      {zones.map((z) => z.name).join(", ")}
                    </span>
                  </span>
                </>
              )}
            </span>
          </label>
        )}
        {!target && (
          <span className="flex flex-col gap-0.5">
            <Check
              label="Keep this hostname's earlier analytics, if it has any"
              checked={!newSite}
              onChange={(keep) => setNewSite(!keep)}
            />
            {/* What the box does either way, so leaving it isn't a guess. */}
            <span className="pl-6 text-xs text-mut">
              {newSite
                ? "Its visitors start from zero in a new site."
                : "If the hostname was counted before, its visitors and errors carry on in the same site."}
            </span>
          </span>
        )}
        <Footer
          className="mt-1"
          submit={
            add.isPending
              ? fromCloudflare
                ? "Setting up…"
                : "Adding…"
              : `Add ${host || "domain"}`
          }
          pending={add.isPending}
          problem={v.problem([!host && "a hostname"], !!hostError)}
          error={add.error}
          cli={[
            `dock domain add ${host || "<host>"}${target ? ` --redirect-to ${target}` : ""}${fromCloudflare ? " --cloudflare" : ""}${newSite && !target ? " --analytics new" : ""} ${flags}`,
          ]}
          onClose={onClose}
        />
      </form>
    </Dialog>
  );
}
