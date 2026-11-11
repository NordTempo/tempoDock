import { Link } from "@tanstack/react-router";
import { useEffect } from "react";
import { useChanges } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { Dialog } from "../../ui/Dialog";
import { ago, clock, day, plural } from "../../ui/format";
import { Button, ErrorText } from "../../ui/kit";
import { notify } from "../../ui/notice";
import { Footer } from "../create/form";
import { useSwitchReferences } from "./data";

/**
 * "Use db-restored" and switching back (ADR 0061): a dry run lists the
 * services whose `${{from.KEY}}` references move to `to`, then the switch
 * applies it and links the releases it made. Refused up front while the
 * environment has other staged changes. Loaded when it opens.
 */
export default function SwitchDialog({
  serviceRef: ref,
  to,
  since,
  onClose,
}: {
  /** The service switched away from. */
  serviceRef: ServiceRef;
  to: string;
  /** When `to`'s restore point was taken, when it is a restored copy. */
  since?: string;
  onClose: () => void;
}) {
  const org = useOrg();
  const staged = useChanges(ref).count;
  const plan = useSwitchReferences();
  const run = useSwitchReferences();
  const from = ref.service;
  // The dry run once per pair (a fresh ref object each render isn't one).
  const { mutate: dryRun } = plan;
  const { project, environment, service } = ref;
  useEffect(() => {
    dryRun({ ref: { project, environment, service }, to, dry_run: true });
  }, [dryRun, project, environment, service, to]);
  const services = plan.data?.services ?? [];
  const cli = `dock service switch-references ${from} --to ${to} -e ${ref.environment}`;

  const done = run.data;
  if (done) {
    return (
      <Dialog title={`Switched ${from} to ${to}`} onClose={onClose}>
        <div className="flex flex-col gap-3">
          <p className="text-sec">
            {(done.releases ?? []).length === 0
              ? "References moved; no running service needed a release."
              : "Config releases started:"}
          </p>
          <ul className="flex flex-col gap-1">
            {(done.releases ?? []).map((r) => (
              <li key={r.service}>
                {r.service}{" "}
                <Link
                  to="/$org/$project/$environment/$service/history/$number"
                  params={{
                    org,
                    project: ref.project,
                    environment: ref.environment,
                    service: r.service,
                    number: String(r.number),
                  }}
                  className="font-mono"
                >
                  r{r.number}
                </Link>
              </li>
            ))}
          </ul>
          <Button className="self-end" onClick={onClose}>
            Close
          </Button>
        </div>
      </Dialog>
    );
  }

  return (
    <Dialog title={`Use ${to} in place of ${from}?`} onClose={onClose}>
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          run.mutate(
            { ref, to },
            {
              onSuccess: (r) =>
                notify(
                  `Switched ${from} to ${to}: ${plural((r.releases ?? []).length, "release")} started`,
                  "info",
                ),
            },
          );
        }}
      >
        {plan.isPending ? (
          <p className="text-mut">Finding the services that use {from}.</p>
        ) : plan.error ? (
          <ErrorText error={plan.error} />
        ) : services.length === 0 ? (
          <p className="text-sec">
            Nothing in {ref.environment} references {from}, so there is nothing
            to switch.
          </p>
        ) : (
          <>
            <p className="text-sec">
              Every <code className="font-mono">{`\${{${from}.KEY}}`}</code>{" "}
              becomes <code className="font-mono">{`\${{${to}.KEY}}`}</code>,
              and these services get a config release:
            </p>
            <p className="font-mono">{services.join(", ")}</p>
            {since && (
              <p className="text-warn-text">
                ▲ Data written to {from} after {day(since)} {clock(since)} (
                {ago(since)}) is not in this copy.
              </p>
            )}
            <p className="text-sec">
              {from} keeps running; switching back is the same action the other
              way.
            </p>
          </>
        )}
        {staged > 0 && (
          <p className="text-fail">
            {ref.environment} has {plural(staged, "change")} waiting to apply.
            Apply or discard {staged === 1 ? "it" : "them"} first: they would go
            live with this.
          </p>
        )}
        <Footer
          className="mt-1"
          submit={
            services.length > 0
              ? `Switch ${plural(services.length, "service")}`
              : "Switch"
          }
          pending={run.isPending || plan.isPending}
          disabled={staged > 0 || !plan.isSuccess || services.length === 0}
          reason={
            staged > 0
              ? "Apply or discard the changes waiting to apply first."
              : plan.error
                ? "The services using it couldn't be listed."
                : "Nothing to switch."
          }
          error={run.error}
          cli={[cli]}
          onClose={onClose}
        />
      </form>
    </Dialog>
  );
}
