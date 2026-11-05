import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { useOrgs } from "../../data/queries";
import {
  autofocus,
  Field,
  Fields,
  Footer,
  input,
  invalid,
  nameProblem,
  toName,
  useValidation,
} from "./form";
import { useCreateOrg } from "./mutations";

/**
 * `dock org create`: a slug for its address and an optional display name,
 * with you as its admin. Opens the new organization.
 */
export function NewOrg({ onClose }: { onClose: () => void }) {
  const orgs = useOrgs();
  const create = useCreateOrg();
  const navigate = useNavigate();
  const [slug, setSlug] = useState("");
  const [name, setName] = useState("");
  const v = useValidation();
  const problem = nameProblem(
    "org",
    slug,
    orgs.map((o) => o.slug),
  );
  const slugError = problem ?? v.required(!slug, "Enter a slug, like acme.");
  return (
    <form
      ref={v.form}
      aria-label="New organization"
      onSubmit={(e) => {
        e.preventDefault();
        if (!v.check(!!slug && !problem)) return;
        create.mutate(
          { slug, ...(name.trim() && { name: name.trim() }) },
          {
            onSuccess: async (o) => {
              await navigate({ to: "/$org", params: { org: o.slug } });
              onClose();
            },
          },
        );
      }}
    >
      <Fields>
        <Field
          label="Slug"
          error={slugError}
          note={`Its address: ${window.location.host}/${slug || "acme"}`}
        >
          <input
            ref={autofocus}
            value={slug}
            onChange={(e) => setSlug(toName(e.target.value))}
            placeholder="acme"
            spellCheck={false}
            autoComplete="off"
            aria-invalid={!!slugError}
            className={`${input} ${invalid(slugError)}`}
          />
        </Field>
        <Field label="Name" note="Optional; the slug if empty.">
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Acme"
            autoComplete="off"
            className={input}
          />
        </Field>
      </Fields>
      <Footer
        submit="Create organization"
        pending={create.isPending}
        problem={v.problem([!slug && "a slug"], !!problem)}
        error={create.error}
        cli={[
          `dock org create ${slug || "<slug>"}${name.trim() ? ` --name ${JSON.stringify(name.trim())}` : ""}`,
        ]}
        onClose={onClose}
      />
    </form>
  );
}
