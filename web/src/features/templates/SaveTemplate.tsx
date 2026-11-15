import { useState } from "react";
import { useProjectOverview } from "../../data/queries";
import { Dialog } from "../../ui/Dialog";
import {
  autofocus,
  Field,
  Fields,
  Footer,
  Group,
  input,
  invalid,
  nameProblem,
  toName,
  useValidation,
} from "../create/form";
import { useSaveTemplateFrom } from "./data";

/**
 * Project settings › Save as template (ADR 0082): an environment's
 * services as the organization's own template, `dock template save`.
 * Services that build from source can't be in one; variables that aren't
 * references become inputs, so no value is copied.
 */
export default function SaveTemplate({
  project,
  onClose,
}: {
  project: string;
  onClose: () => void;
}) {
  const overview = useProjectOverview(project);
  const envs = overview.environments;
  const [environment, setEnvironment] = useState(
    envs[0]?.environment.name ?? "",
  );
  const services = (
    envs.find((e) => e.environment.name === environment)?.services ?? []
  ).map((s) => s.service);
  const savable = services.filter((s) => s.image || s.template);
  const [picked, setPicked] = useState<string[] | null>(null);
  const chosen = picked ?? savable.map((s) => s.name);
  const [name, setName] = useState(project);
  const save = useSaveTemplateFrom();
  const v = useValidation();
  const nameError =
    nameProblem("service", name) ?? v.required(!name, "Enter a name.");
  const missing = [!name && "a name", chosen.length === 0 && "a service"];
  const blocked = missing.some(Boolean) || !!nameProblem("service", name);
  const cli = [
    `dock template save ${name || "<name>"} -p ${project} -e ${environment}${picked ? ` --services ${chosen.join(",")}` : ""}`,
  ];

  return (
    <Dialog title="Save as template" onClose={onClose} wide>
      <form
        ref={v.form}
        aria-label="Save as template"
        onSubmit={(e) => {
          e.preventDefault();
          if (!v.check(!blocked)) return;
          save.mutate(
            {
              env: { project, environment },
              name,
              ...(picked && { services: chosen }),
            },
            { onSuccess: onClose },
          );
        }}
      >
        <Fields>
          <Field
            label="Name"
            error={nameError}
            note="The organization's members find it under New service, Template."
          >
            <input
              ref={autofocus}
              value={name}
              onChange={(e) => setName(toName(e.target.value))}
              spellCheck={false}
              autoComplete="off"
              aria-invalid={!!nameError}
              className={`${input} ${invalid(nameError)}`}
            />
          </Field>
          {envs.length > 1 && (
            <Field label="Environment">
              <select
                value={environment}
                onChange={(e) => {
                  setEnvironment(e.target.value);
                  setPicked(null);
                }}
                className="w-fit font-mono"
              >
                {envs.map((e) => (
                  <option key={e.environment.name} value={e.environment.name}>
                    {e.environment.name}
                  </option>
                ))}
              </select>
            </Field>
          )}
          <Group
            label="Services"
            note="Secrets become inputs it asks for; their values aren't copied."
          >
            {services.map((s) => {
              const ok = !!(s.image || s.template);
              return (
                <label
                  key={s.name}
                  className={`flex items-center gap-1.5 font-mono text-xs ${ok ? "" : "text-faint"}`}
                  title={
                    ok
                      ? undefined
                      : "Builds from source; a template runs images"
                  }
                >
                  <input
                    type="checkbox"
                    disabled={!ok}
                    checked={chosen.includes(s.name)}
                    onChange={(e) =>
                      setPicked(
                        e.target.checked
                          ? [...chosen, s.name]
                          : chosen.filter((n) => n !== s.name),
                      )
                    }
                  />
                  {s.name}
                </label>
              );
            })}
          </Group>
        </Fields>
        <Footer
          submit="Save template"
          pending={save.isPending}
          problem={v.problem(missing, blocked)}
          error={save.error}
          cli={cli}
          onClose={onClose}
        />
      </form>
    </Dialog>
  );
}
