import { useState } from "react";
import { useSession } from "../../data/queries";
import { Dialog } from "../../ui/Dialog";
import { Check } from "../../ui/kit";
import { Footer, invalid, useValidation } from "../create/form";
import type { Finding, ImageUse } from "./data";
import {
  type IgnoreRequest,
  type IgnoreScope,
  ignoreCli,
  mayIgnore,
  useIgnoreVuln,
  where,
} from "./ignores";

// Ignores one vulnerability from an image's All table (ADR 0074, mock
// I1): where (a service that runs the image, its project, or the whole
// organization), only in its package (prefilled), why, and until when.

const untils = [
  { days: 30, label: "30 days" },
  { days: 90, label: "90 days" },
  { days: 365, label: "a year" },
  { days: 0, label: "always" },
] as const;

type Place = {
  key: string;
  scope: IgnoreScope;
  project?: string;
  environment?: string;
  service?: string;
};

/** The places an image's finding can be ignored: its services, their projects, the org. */
function placesOf(uses: ImageUse[]): Place[] {
  const out: Place[] = [];
  const seen = new Set<string>();
  const add = (p: Place) => {
    if (seen.has(p.key)) return;
    seen.add(p.key);
    out.push(p);
  };
  for (const u of uses)
    add({
      key: `s:${u.project}/${u.environment}/${u.service}`,
      scope: "service",
      project: u.project,
      environment: u.environment,
      service: u.service,
    });
  for (const u of uses)
    add({ key: `p:${u.project}`, scope: "project", project: u.project });
  add({ key: "org", scope: "org" });
  return out;
}

export default function IgnoreDialog({
  finding,
  uses,
  onClose,
}: {
  finding: Finding;
  /** The releases that run the image, live ones first. */
  uses: ImageUse[];
  onClose: () => void;
}) {
  const session = useSession();
  const ignore = useIgnoreVuln();
  const live = uses.filter((u) => u.live);
  // Production first: it is what an ignore is most often about.
  const ordered = (live.length > 0 ? live : uses).toSorted(
    (a, b) => Number(b.production) - Number(a.production),
  );
  const places = placesOf(ordered).filter((p) => mayIgnore(session, p));
  const [placeKey, setPlace] = useState(places[0]?.key ?? "");
  const place = places.find((p) => p.key === placeKey) ?? places[0];
  const [onlyPackage, setOnlyPackage] = useState(true);
  const [reason, setReason] = useState("");
  const [days, setDays] = useState<number>(90);
  const admin = session?.scope === "admin";
  const v = useValidation();
  const reasonError = v.required(
    !reason.trim(),
    "Say why it doesn't matter here.",
  );
  const req: IgnoreRequest = {
    vulnerability: finding.id,
    scope: place?.scope ?? "service",
    project: place?.project,
    environment: place?.environment,
    service: place?.service,
    reason: reason.trim(),
    ...(onlyPackage && { package: finding.package }),
    ...(days === 0 ? { always: true } : { days }),
  };
  return (
    <Dialog title={`Ignore ${finding.id}?`} onClose={onClose} wide>
      <form
        ref={v.form}
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (!place || !v.check(!!reason.trim())) return;
          ignore.mutate(req, { onSuccess: onClose });
        }}
      >
        <p className="text-mut">
          It stops counting and alerting where you choose. It is still listed,
          as ignored.
        </p>
        <div className="grid grid-cols-[110px_1fr] items-start gap-x-3 gap-y-3">
          <span className="text-mut">Where</span>
          <span
            role="radiogroup"
            aria-label="Where"
            className="flex flex-col gap-1"
          >
            {places.map((p) => (
              <label
                key={p.key}
                className="inline-flex cursor-pointer items-center gap-1.5"
              >
                <input
                  type="radio"
                  name="where"
                  className="m-[4px] size-[16px] accent-fg"
                  checked={p.key === place?.key}
                  onChange={() => setPlace(p.key)}
                />
                {where(p)}
              </label>
            ))}
          </span>
          <span className="text-mut">Package</span>
          <Check
            label={
              <>
                Only in <span className="font-mono">{finding.package}</span>
              </>
            }
            checked={onlyPackage}
            onChange={setOnlyPackage}
          />
          <span className="pt-[7px] text-mut">Why</span>
          <span className="flex flex-col gap-1">
            <textarea
              aria-label="Why"
              aria-invalid={!!reasonError}
              data-autofocus
              rows={2}
              maxLength={500}
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder="Untrusted input never reaches it."
              className={`field ${invalid(reasonError)}`}
            />
            {reasonError && (
              <span role="alert" className="text-xs text-fail">
                {reasonError}
              </span>
            )}
          </span>
          <span className="text-mut">Until</span>
          <span
            role="radiogroup"
            aria-label="Until"
            className="flex flex-wrap gap-x-4 gap-y-1"
          >
            {untils.map((u) => (
              <label
                key={u.days}
                className={`inline-flex items-center gap-1.5 ${u.days === 0 && !admin ? "text-faint" : "cursor-pointer"}`}
                title={
                  u.days === 0 && !admin
                    ? "Only admins ignore for good"
                    : undefined
                }
              >
                <input
                  type="radio"
                  name="until"
                  className="m-[4px] size-[16px] accent-fg"
                  disabled={u.days === 0 && !admin}
                  checked={days === u.days}
                  onChange={() => setDays(u.days)}
                />
                {u.label}
                {u.days === 0 && <span className="text-faint">(admins)</span>}
              </label>
            ))}
          </span>
        </div>
        <Footer
          className="mt-1"
          submit={ignore.isPending ? "Ignoring…" : "Ignore"}
          pending={ignore.isPending}
          disabled={!place}
          reason="You can't ignore it anywhere this image runs."
          problem={v.problem([!reason.trim() && "a reason"])}
          error={ignore.error}
          cli={[ignoreCli(req)]}
          onClose={onClose}
        />
      </form>
    </Dialog>
  );
}
