import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useCloneEnvironment } from "../../data/mutations";
import { useIsAdmin } from "../../data/queries";
import type { Clone, EnvironmentKind, EnvRef } from "../../data/types";
import { plural } from "../../ui/format";
import { Button, Check, ErrorText } from "../../ui/kit";
import { kindLabel } from "../../ui/status";

/** The kind an environment gets by its name, as the API decides it. */
const kindOf = (name: string): EnvironmentKind =>
  name === "production" ? "production" : "non_production";

const names = (vs: Clone["skipped"]) =>
  (vs ?? []).map((v) => `${v.service || "shared"}.${v.key}`).join(", ");

/**
 * "Clone" on an environment (ADR 0022): a name, a kind, optionally deploy
 * the same images, Clone. Into the other kind, literal values stay behind
 * unless an admin ticks "copy values". The answer says where it went,
 * which values still need setting and what a clone leaves behind.
 */
export function CloneEnvironment({
  env,
  kind: sourceKind,
  onClose,
}: {
  env: EnvRef;
  /** The kind of the environment being cloned. */
  kind: EnvironmentKind;
  onClose: () => void;
}) {
  const clone = useCloneEnvironment();
  const isAdmin = useIsAdmin();
  const [name, setName] = useState(`${env.environment}-copy`);
  const [kind, setKind] = useState<EnvironmentKind | null>(null);
  const [deploy, setDeploy] = useState(false);
  const [copyValues, setCopyValues] = useState(false);
  const effective = kind ?? kindOf(name.trim());
  const cross = effective !== sourceKind;

  if (clone.data) {
    return <Cloned env={env} result={clone.data} onClose={onClose} />;
  }

  return (
    <form
      aria-label="Clone environment"
      className="flex flex-wrap items-center gap-2.5 py-2"
      onSubmit={(e) => {
        e.preventDefault();
        clone.mutate({
          env,
          name: name.trim(),
          kind: effective,
          deploy,
          copy_values: isAdmin && cross && copyValues,
        });
      }}
      onKeyDown={(e) => e.key === "Escape" && onClose()}
    >
      <input
        aria-label="New environment name"
        // biome-ignore lint/a11y/noAutofocus: the form opens on request
        autoFocus
        value={name}
        onChange={(e) => setName(e.target.value)}
        pattern="[a-z0-9]([a-z0-9\-]{0,30}[a-z0-9])?"
        title="Lower-case letters, digits and dashes"
        className="field w-[180px] py-[5px] font-mono"
      />
      <select
        aria-label="Kind"
        className="field py-[5px]"
        value={effective}
        onChange={(e) => setKind(kindOf(e.target.value))}
      >
        <option value="non_production">{kindLabel.non_production}</option>
        <option value="production">{kindLabel.production}</option>
      </select>
      <Check
        label="deploy the same images"
        checked={deploy}
        onChange={setDeploy}
      />
      {isAdmin && cross && (
        <Check
          label={`copy values from ${kindLabel[sourceKind]}`}
          title="Literal values, such as secrets and external database URLs, stay behind when a clone changes kind unless this is ticked"
          checked={copyValues}
          onChange={setCopyValues}
        />
      )}
      <button
        type="submit"
        className="btn pri sm"
        disabled={clone.isPending || !name.trim()}
      >
        Clone
      </button>
      <Button small onClick={onClose}>
        Cancel
      </Button>
      <span className="text-mut">
        Services, settings, variables and new empty volumes. No domains,
        releases or backups.
      </span>
      <ErrorText error={clone.error} />
    </form>
  );
}

/** What a clone did, with the values left to set. */
function Cloned({
  env,
  result,
  onClose,
}: {
  env: EnvRef;
  result: Clone;
  onClose: () => void;
}) {
  const e = result.environment;
  const to = { project: env.project, environment: e.name };
  const skipped = names(result.skipped);
  const copied = names(result.copied_across_kinds);
  const promoted = result.deploy?.promoted?.length ?? 0;
  return (
    <div className="flex flex-col gap-1 py-2">
      <p className="flex flex-wrap items-center gap-2.5">
        <span>
          Cloned into{" "}
          <Link to="/$project/$environment" params={to} className="font-mono">
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
        </span>
        <Button small onClick={onClose}>
          Done
        </Button>
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
    </div>
  );
}
