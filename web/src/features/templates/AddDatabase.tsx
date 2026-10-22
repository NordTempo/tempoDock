import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useCreateDatabase } from "../../data/mutations";
import { queries } from "../../data/queries";
import type { EnvRef, TemplateInfo } from "../../data/types";
import { Button, CopyButton, ErrorText } from "../../ui/kit";

type Created = { name: string; reference: string };

/**
 * "Add database" on an environment: a template, a name, Create. The
 * database deploys right away; the answer shows the variable reference
 * other services use.
 */
export function AddDatabase({
  env,
  onClose,
}: {
  env: EnvRef;
  onClose: () => void;
}) {
  const templates = useQuery(queries.templates());
  const create = useCreateDatabase();
  const [chosen, setChosen] = useState<TemplateInfo["name"]>("postgres");
  const [name, setName] = useState<string | null>(null);
  const [created, setCreated] = useState<Created | null>(null);
  const template = templates.data?.find((t) => t.name === chosen);
  const service = name ?? chosen;

  if (created) {
    return (
      <p className="flex flex-wrap items-center gap-2.5 py-2">
        <span>
          Created{" "}
          <Link
            to="/$project/$environment/$service"
            params={{ ...env, service: created.name }}
            className="font-mono"
          >
            {created.name}
          </Link>
          , deploying.
          {created.reference && (
            <>
              {" "}
              Other services use{" "}
              <code className="font-mono">{created.reference}</code>
            </>
          )}
        </span>
        {created.reference && <CopyButton text={created.reference} />}
        <Button small autoFocus onClick={onClose}>
          Done
        </Button>
      </p>
    );
  }

  return (
    <form
      aria-label="Add database"
      className="flex flex-wrap items-center gap-2.5 py-2"
      onSubmit={(e) => {
        e.preventDefault();
        const exported = template?.exports[0];
        create.mutate(
          { env, name: service.trim(), template: chosen },
          {
            onSuccess: (svc) =>
              setCreated({
                name: svc.name,
                reference: exported ? `\${{${svc.name}.${exported}}}` : "",
              }),
          },
        );
      }}
      onKeyDown={(e) => e.key === "Escape" && onClose()}
    >
      <select
        aria-label="Template"
        className="field py-[3px]"
        value={chosen}
        onChange={(e) => {
          const t = templates.data?.find((t) => t.name === e.target.value);
          if (t) setChosen(t.name);
        }}
      >
        {(templates.data ?? []).map((t) => (
          <option key={t.name} value={t.name}>
            {t.name}
          </option>
        ))}
      </select>
      <input
        aria-label="Service name"
        // biome-ignore lint/a11y/noAutofocus: the form opens on request
        autoFocus
        value={service}
        onChange={(e) => setName(e.target.value)}
        pattern="[a-z0-9]([a-z0-9\-]{0,30}[a-z0-9])?"
        title="Lower-case letters, digits and dashes"
        className="field w-[180px] py-[3px] font-mono"
      />
      <button
        type="submit"
        className="btn pri sm"
        disabled={create.isPending || !template || !service.trim()}
      >
        Create
      </button>
      <Button small onClick={onClose}>
        Cancel
      </Button>
      {template && (
        <span className="text-mut">
          {template.description}, a volume at{" "}
          <span className="font-mono">{template.mount_path}</span>, generated
          credentials, daily backups.
        </span>
      )}
      <code className="font-mono text-2xs text-mut">
        dock service create {service.trim() || "<name>"} --template {chosen} -e{" "}
        {env.environment}
      </code>
      <ErrorText error={templates.error ?? create.error} />
    </form>
  );
}
