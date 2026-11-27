import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useProjectOverview } from "../../data/queries";
import type { EnvRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { Dialog } from "../../ui/Dialog";
import { plural } from "../../ui/format";
import { Act, Button, Check, ErrorText } from "../../ui/kit";
import {
  ComposeInputs,
  ConversionNotes,
  type EnvFiles,
  Section,
  secretValues,
  withEnvFile,
} from "./ComposeParts";
import { composeRows, plannedServices } from "./composePlan";
import { useImportCompose } from "./data";
import { Rows } from "./PlanView";

/**
 * Update from Compose: `dock import compose -e ENV --stage` on an
 * existing environment (ADR 0049, amended 2026-11-27). Paste or load the
 * newer Compose file, see the services it adds, changes and leaves out,
 * and stage it; the environment's changes are then reviewed and applied
 * there. Removing is one explicit checkbox (prune, admin). Loaded on
 * first open, from the environment's Settings.
 */
export default function UpdateFromCompose({
  env,
  onClose,
}: {
  env: EnvRef;
  onClose: () => void;
}) {
  const org = useOrg();
  const overview = useProjectOverview(env.project);
  const [compose, setCompose] = useState("");
  const [fileName, setFileName] = useState("docker-compose.yml");
  const [envFiles, setEnvFiles] = useState<EnvFiles>({});
  const [values, setValues] = useState<Record<string, string>>({});
  const [prune, setPrune] = useState(false);
  const preview = useImportCompose();
  const stage = useImportCompose();
  const busy = preview.isPending || stage.isPending;

  // The preview shown is for what is in the form now: an edit to the
  // files or the checkbox makes it stale (values aren't part of it).
  const last = preview.variables;
  const shown =
    last?.compose === compose &&
    last.env_files === envFiles &&
    !!last.prune === prune
      ? preview.data
      : undefined;
  const request = (files: EnvFiles) => ({
    project: env.project,
    environment: env.environment,
    compose,
    env_files: files,
    prune,
    values: secretValues(shown?.secrets ?? [], values),
  });
  const cli = `dock import compose ${fileName} -p ${env.project} -e ${env.environment}${prune ? " --prune" : ""}`;

  const staged = stage.data?.result;
  const plan = staged?.plan ?? shown?.plan;
  const changes = composeRows(plan?.changes ?? [], "changes");
  const kept = composeRows(plan?.kept ?? [], "kept");
  const manual = composeRows(plan?.manual ?? [], "manual");
  const named = plannedServices([
    plan?.changes ?? [],
    plan?.kept ?? [],
    plan?.manual ?? [],
  ]);
  const unchanged = (
    overview.environments.find((e) => e.environment.name === env.environment)
      ?.services ?? []
  )
    .map((s) => s.service.name)
    .filter((name) => !named.has(name));
  const leftOut = (plan?.kept ?? []).filter((c) => c.kind === "service");

  return (
    <Dialog
      title={`Update ${env.environment} from Compose`}
      onClose={onClose}
      wide
    >
      <div className="flex flex-col gap-3.5">
        <ComposeInputs
          compose={compose}
          onCompose={setCompose}
          onFileName={setFileName}
          envFiles={envFiles}
          onEnvFiles={setEnvFiles}
        />
        <Check
          label={
            <>
              archive services the file leaves out{" "}
              <span className="text-mut">
                (volumes and backups kept; needs an admin)
              </span>
            </>
          }
          checked={prune}
          onChange={setPrune}
          disabled={!!staged}
        />
        <div className="flex flex-wrap items-center gap-2">
          <Act
            cli={cli}
            disabled={!compose.trim() || busy || !!staged}
            onClick={() => preview.mutate(request(envFiles))}
          >
            Preview
          </Act>
          <Act
            primary
            cli={`${cli} --stage`}
            disabled={!shown || busy || !!staged || changes.length === 0}
            onClick={() =>
              stage.mutate({
                ...request(envFiles),
                apply: true,
                stage_only: true,
              })
            }
          >
            {shown && changes.length > 0
              ? `Stage ${plural(changes.length, "change")}`
              : "Stage"}
          </Act>
          <Button onClick={onClose}>{staged ? "Done" : "Cancel"}</Button>
          <ErrorText error={preview.error ?? stage.error} />
        </div>
        {staged &&
          (staged.staged ?? []).map((p) => (
            <p key={p.environment} className="flex flex-wrap gap-x-3 text-xs">
              {p.count > 0 ? (
                <>
                  <span className="text-warn">
                    {plural(p.count, "change")} waiting to apply in{" "}
                    {p.environment}
                  </span>
                  <Link
                    to="/$org/$project/settings/variables/$environment"
                    params={{
                      org,
                      project: env.project,
                      environment: p.environment,
                    }}
                    className="lnk"
                    onClick={onClose}
                  >
                    Review and apply
                  </Link>
                </>
              ) : (
                <span className="text-mut">
                  nothing to stage: {p.environment} matches the file
                </span>
              )}
            </p>
          ))}
        {shown && plan && (
          <div className="flex max-h-[55vh] flex-col gap-3 overflow-y-auto border-t border-line pt-3">
            {!staged && (
              <ConversionNotes
                shown={shown}
                project={env.project}
                values={values}
                onValues={setValues}
                onAddFile={(path, text) => {
                  const files = withEnvFile(envFiles, path, text);
                  setEnvFiles(files);
                  preview.mutate(request(files));
                }}
              />
            )}
            {changes.length === 0 ? (
              <p className="text-xs text-mut">
                <span className="text-ok">✓</span> No changes: {env.environment}{" "}
                matches the Compose file.
              </p>
            ) : (
              <Rows title={plural(changes.length, "change")} rows={changes} />
            )}
            {leftOut.length > 0 && (
              <p className="text-xs text-mut">
                {plural(leftOut.length, "service")} the file leaves out{" "}
                {leftOut.length === 1 ? "is" : "are"} kept; tick archive above
                to remove {leftOut.length === 1 ? "it" : "them"}.
              </p>
            )}
            {kept.length > 0 && (
              <Rows title="Not in the Compose file, kept" rows={kept} />
            )}
            {manual.length > 0 && <Rows title="Needs you" rows={manual} />}
            {unchanged.length > 0 && (
              <Section title="Unchanged">
                <p className="font-mono text-mut">{unchanged.join(", ")}</p>
              </Section>
            )}
          </div>
        )}
      </div>
    </Dialog>
  );
}
