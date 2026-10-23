import { Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { useCloneEnvironment } from "../../data/mutations";
import { useProjectOverview, useSession } from "../../data/queries";
import type { Clone, EnvironmentKind } from "../../data/types";
import { useOrg } from "../../shell/location";
import { plural } from "../../ui/format";
import { Button, Check } from "../../ui/kit";
import { kindLabel } from "../../ui/status";
import {
  autofocus,
  Choice,
  Field,
  Fields,
  Footer,
  Group,
  input,
  nameProblem,
  toName,
} from "./form";
import { useCreateEnvironment } from "./mutations";

/** The kind an environment gets by its name, as the API decides it. */
const kindOf = (name: string): EnvironmentKind =>
  name === "production" ? "production" : "non_production";

const kinds = [
  ["non_production", kindLabel.non_production],
  ["production", kindLabel.production],
] as const;

/**
 * `dock environment create` and `dock environment clone` side by side:
 * a name, a kind, and whether it starts empty or as a copy of another
 * environment (ADR 0022). Into the other kind, a clone leaves literal
 * values behind unless an admin ticks "copy values".
 */
export function NewEnvironment({
  project,
  cloneFrom,
  onClose,
}: {
  project: string;
  /** Start as a clone of this environment. */
  cloneFrom?: string;
  onClose: () => void;
}) {
  const org = useOrg();
  const environments = useProjectOverview(project).environments.map(
    (e) => e.environment,
  );
  const isAdmin = useSession()?.scope === "admin";
  const create = useCreateEnvironment();
  const clone = useCloneEnvironment();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [kind, setKind] = useState<EnvironmentKind | null>(null);
  const [start, setStart] = useState<"empty" | "clone">(
    cloneFrom ? "clone" : "empty",
  );
  const [from, setFrom] = useState(
    cloneFrom ?? environments[0]?.name ?? "production",
  );
  const [deploy, setDeploy] = useState(false);
  const [copyValues, setCopyValues] = useState(false);

  const effective = kind ?? kindOf(name);
  const source = environments.find((e) => e.name === from);
  const cloning = start === "clone" && !!source;
  const cross = cloning && source.kind !== effective;
  const problem = nameProblem(
    "environment",
    name,
    environments.map((e) => e.name),
  );

  if (clone.data) {
    return <Cloned project={project} result={clone.data} onClose={onClose} />;
  }

  const kindFlag =
    effective === kindOf(name) ? "" : ` --kind ${kindLabel[effective]}`;
  const cli = cloning
    ? `dock environment clone ${from} ${name || "<name>"}${kindFlag}${deploy ? " --deploy" : ""}${cross && copyValues ? " --copy-values" : ""} -p ${project}`
    : `dock environment create ${name || "<name>"}${kindFlag} -p ${project}`;
  const pending = create.isPending || clone.isPending;

  return (
    <form
      aria-label="New environment"
      onSubmit={(e) => {
        e.preventDefault();
        if (cloning) {
          clone.mutate({
            env: { project, environment: from },
            name,
            kind: effective,
            deploy,
            copy_values: isAdmin && cross && copyValues,
          });
          return;
        }
        create.mutate(
          { project, name, kind: effective },
          {
            onSuccess: async () => {
              await navigate({
                to: "/$org/$project",
                params: { org, project },
              });
              onClose();
            },
          },
        );
      }}
    >
      <Fields>
        <Field
          label="Name"
          note={problem && <span className="text-fail">{problem}</span>}
        >
          <input
            ref={autofocus}
            value={name}
            onChange={(e) => setName(toName(e.target.value))}
            placeholder="staging"
            spellCheck={false}
            autoComplete="off"
            className={input}
          />
        </Field>
        <Group label="Kind">
          <Choice
            label="Kind"
            name="kind"
            value={effective}
            options={kinds}
            onChange={setKind}
          />
          <span className="text-xs text-mut">never changes</span>
        </Group>
        {environments.length > 0 && (
          <Group label="Start">
            <Choice
              label="Start"
              name="start"
              value={start}
              options={[
                ["empty", "Empty"],
                ["clone", "Clone of"],
              ]}
              onChange={setStart}
            />
            {start === "clone" && (
              <select
                aria-label="Environment to clone"
                value={from}
                onChange={(e) => setFrom(e.target.value)}
                className="field py-[3px] font-mono"
              >
                {environments.map((e) => (
                  <option key={e.id} value={e.name}>
                    {e.name}
                  </option>
                ))}
              </select>
            )}
          </Group>
        )}
        {cloning && (
          <Group label="">
            <Check
              label="deploy the same images"
              checked={deploy}
              onChange={setDeploy}
            />
            {isAdmin && cross && (
              <Check
                label={`copy values from ${kindLabel[source.kind]}`}
                title="Literal values, such as secrets and external database URLs, stay behind when a clone changes kind unless this is ticked"
                checked={copyValues}
                onChange={setCopyValues}
              />
            )}
          </Group>
        )}
        <span className="col-start-2 text-xs text-mut">
          {cloning
            ? "Services, settings, variables and new empty volumes. No domains, releases or backups."
            : "No services yet. Placed on a server automatically."}
        </span>
      </Fields>
      <Footer
        submit={cloning ? `Clone ${from}` : "Create environment"}
        pending={pending}
        disabled={!name || !!problem}
        error={create.error ?? clone.error}
        cli={[cli]}
        onClose={onClose}
      />
    </form>
  );
}

const names = (vs: Clone["skipped"]) =>
  (vs ?? []).map((v) => `${v.service || "shared"}.${v.key}`).join(", ");

/** What a clone did, with the values left to set. */
function Cloned({
  project,
  result,
  onClose,
}: {
  project: string;
  result: Clone;
  onClose: () => void;
}) {
  const org = useOrg();
  const e = result.environment;
  const skipped = names(result.skipped);
  const copied = names(result.copied_across_kinds);
  const promoted = result.deploy?.promoted?.length ?? 0;
  return (
    <div className="flex flex-col gap-2">
      <p>
        Cloned {result.from} into{" "}
        <Link
          to="/$org/$project/$environment"
          params={{ org, project, environment: e.name }}
          className="font-mono"
          onClick={onClose}
        >
          {e.name}
        </Link>{" "}
        on {e.server}: {plural(result.services?.length ?? 0, "service")},{" "}
        {plural(result.variables, "variable")},{" "}
        {plural(result.volumes, "new volume")}.{" "}
        {result.deploy_error
          ? `Nothing deployed: ${result.deploy_error}`
          : result.deploy
            ? `Deploying ${plural(promoted, "service")}.`
            : "Nothing deployed."}
      </p>
      {copied && (
        <p className="text-warn">
          Values copied from {kindLabel[result.source_kind]} {result.from}:{" "}
          <span className="font-mono">{copied}</span>
        </p>
      )}
      {skipped && (
        <p className="text-warn">
          Values not copied: <span className="font-mono">{skipped}</span>. Set
          them before deploying.
        </p>
      )}
      {(result.generated?.length ?? 0) > 0 && (
        <p className="text-mut">
          New values:{" "}
          <span className="font-mono">{names(result.generated)}</span>
        </p>
      )}
      <p className="text-mut">
        Not copied: {(result.not_copied ?? []).join(", ")}.
      </p>
      <div className="mt-2">
        <Button primary ref={autofocus} onClick={onClose}>
          Done
        </Button>
      </div>
    </div>
  );
}
