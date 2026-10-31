import { useState } from "react";
import { useAddDomain } from "../../data/mutations";
import type { ServiceRef } from "../../data/types";
import { Dialog } from "../../ui/Dialog";
import { Act, Button, Check, ErrorText } from "../../ui/kit";
import { useCloudflareZones } from "../cloudflare/data";

/**
 * Adds a hostname: your own (you create its DNS record), or from a zone
 * connected to Dock, which Dock routes through the server's Cloudflare
 * tunnel (ADR 0036).
 */
export default function AddDomainDialog({
  serviceRef: ref,
  onClose,
}: {
  serviceRef: ServiceRef;
  onClose: () => void;
}) {
  const add = useAddDomain();
  const zonesQuery = useCloudflareZones();
  const zones = zonesQuery.data ?? [];
  const [mode, setMode] = useState<"own" | "cloudflare">("own");
  const [hostname, setHostname] = useState("");
  const [redirect, setRedirect] = useState("");
  const [name, setName] = useState("");
  const [zone, setZone] = useState("");
  // A hostname that collected analytics before carries its site's history
  // here unless this is set (ADR 0037).
  const [newSite, setNewSite] = useState(false);
  const picked = zone || zones[0]?.name || "";
  // A full hostname typed into the name field keeps one zone.
  const sub = name
    .trim()
    .replace(/\.$/, "")
    .replace(new RegExp(`(^|\\.)${picked.replaceAll(".", "\\.")}$`), "");
  const fromCloudflare = mode === "cloudflare";
  const host = fromCloudflare
    ? sub
      ? `${sub}.${picked}`
      : picked
    : hostname.trim();
  const target = fromCloudflare ? "" : redirect.trim();
  const flags = `-s ${ref.service} -e ${ref.environment}`;
  const option = (on: boolean) =>
    `flex gap-2.5 rounded-ctl border p-2.5 ${on ? "border-edge bg-sel" : "border-line"}`;
  return (
    <Dialog title={`Add a domain to ${ref.service}`} onClose={onClose} wide>
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          add.mutate(
            {
              ref,
              hostname: host,
              ...(target && { redirect_to: target }),
              ...(fromCloudflare && { cloudflare: true }),
              ...(newSite && !target && { analytics: "new" as const }),
            },
            { onSuccess: onClose },
          );
        }}
      >
        <label className={option(!fromCloudflare)}>
          <input
            type="radio"
            name="mode"
            checked={!fromCloudflare}
            onChange={() => setMode("own")}
          />
          <span className="flex min-w-0 flex-1 flex-col gap-2">
            <span>
              Your own hostname
              <span className="block text-mut">
                You create its DNS record; Dock shows which.
              </span>
            </span>
            {!fromCloudflare && (
              <span className="flex flex-wrap gap-2">
                <input
                  aria-label="Hostname"
                  data-autofocus
                  value={hostname}
                  onChange={(e) => setHostname(e.target.value)}
                  placeholder="app.example.com"
                  className="field min-w-0 flex-[1_1_220px] py-[7px] font-mono"
                />
                <input
                  aria-label="Redirect to, optional"
                  title="Permanently redirect to this hostname instead of serving the service"
                  value={redirect}
                  onChange={(e) => setRedirect(e.target.value)}
                  placeholder="redirect to, optional"
                  className="field min-w-0 flex-[1_1_160px] py-[7px] font-mono"
                />
              </span>
            )}
          </span>
        </label>
        <label className={option(fromCloudflare)}>
          <input
            type="radio"
            name="mode"
            checked={fromCloudflare}
            disabled={zones.length === 0}
            onChange={() => setMode("cloudflare")}
          />
          <span className="flex min-w-0 flex-1 flex-col gap-2">
            <span>
              From Cloudflare
              <span className="block text-mut">
                {zonesQuery.isPending
                  ? "Looking up the connected zones…"
                  : zonesQuery.error
                    ? `Can't list the connected zones: ${zonesQuery.error.message}`
                    : zones.length === 0
                      ? "No zone is connected to Dock; an operator adds one in Console › Cloudflare."
                      : "Dock creates the record and routes it through the server's tunnel."}
              </span>
            </span>
            {fromCloudflare && (
              <>
                <span className="flex flex-wrap items-center gap-2">
                  <input
                    aria-label="Name"
                    data-autofocus
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder="shop, or empty for the zone"
                    className="field min-w-0 flex-[1_1_180px] py-[7px] font-mono"
                  />
                  <span className="text-mut">.</span>
                  <select
                    aria-label="Zone"
                    value={picked}
                    onChange={(e) => setZone(e.target.value)}
                    className="field py-[7px] font-mono"
                  >
                    {zones.map((z) => (
                      <option key={z.name} value={z.name}>
                        {z.name}
                      </option>
                    ))}
                  </select>
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
        {!target && (
          <Check
            label="Start a new analytics site if this hostname was counted before"
            title="Otherwise its site, with its history, moves here with it"
            checked={newSite}
            onChange={setNewSite}
          />
        )}
        <ErrorText error={add.error} />
        <div className="flex items-center justify-end gap-2.5">
          <Button onClick={onClose}>Cancel</Button>
          <Act
            primary
            type="submit"
            cli={`dock domain add ${host || "<host>"}${target ? ` --redirect-to ${target}` : ""}${fromCloudflare ? " --cloudflare" : ""}${newSite && !target ? " --analytics new" : ""} ${flags}`}
            disabled={add.isPending || !host}
          >
            {add.isPending
              ? fromCloudflare
                ? "Setting up…"
                : "Adding…"
              : `Add ${host || "domain"}`}
          </Act>
        </div>
      </form>
    </Dialog>
  );
}
