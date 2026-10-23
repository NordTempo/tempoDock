import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { useOrgs } from "../../data/queries";
import {
  autofocus,
  Field,
  Fields,
  Footer,
  input,
  nameProblem,
  toName,
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
  const problem = nameProblem(
    "org",
    slug,
    orgs.map((o) => o.slug),
  );
  return (
    <form
      aria-label="New organization"
      onSubmit={(e) => {
        e.preventDefault();
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
          note={
            problem ? (
              <span className="text-fail">{problem}</span>
            ) : (
              `Its address: ${window.location.host}/${slug || "acme"}`
            )
          }
        >
          <input
            ref={autofocus}
            value={slug}
            onChange={(e) => setSlug(toName(e.target.value))}
            placeholder="acme"
            spellCheck={false}
            autoComplete="off"
            className={input}
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
        disabled={!slug || !!problem}
        error={create.error}
        cli={[
          `dock org create ${slug || "<slug>"}${name.trim() ? ` --name ${JSON.stringify(name.trim())}` : ""}`,
        ]}
        onClose={onClose}
      />
    </form>
  );
}
