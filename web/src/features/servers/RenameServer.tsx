import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { api, body } from "../../api/client";
import { useAction } from "../../data/mutations";
import { keys, useServers } from "../../data/queries";
import type { Server } from "../../data/types";
import { Dialog } from "../../ui/Dialog";
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
} from "../create/form";

/**
 * `dock server rename`: a new name for a server. Its environments, link
 * and history stay; the page moves to the new name. Loads when opened.
 */
export default function RenameServer({
  server,
  onClose,
}: {
  server: Server;
  onClose: () => void;
}) {
  const client = useQueryClient();
  const navigate = useNavigate();
  const servers = useServers();
  const [name, setName] = useState(server.name);
  const rename = useAction(
    async (name: string) => {
      const updated = await body(
        api.PATCH("/api/v1/servers/{server}", {
          params: { path: { server: server.name } },
          body: { name },
        }),
      );
      await navigate({
        to: "/console/servers/$server",
        params: { server: updated.name },
        search: true,
        replace: true,
      });
      client.removeQueries({ queryKey: keys.server(server.name) });
      client.removeQueries({ queryKey: ["stats", "server", server.name] });
      return updated;
    },
    // Environments name their server.
    () => [keys.servers, ["project"], ["audit"]],
  );
  const v = useValidation();
  const problem = nameProblem(
    "server",
    name,
    servers.filter((s) => s.id !== server.id).map((s) => s.name),
  );
  const same = name === server.name;
  const nameError =
    problem ??
    v.required(!name, "Enter the new name.") ??
    v.required(same, "Type a name different from the current one.");
  return (
    <Dialog title="Rename server" onClose={onClose}>
      <form
        ref={v.form}
        aria-label="Rename server"
        onSubmit={(event) => {
          event.preventDefault();
          if (rename.isPending || !v.check(!!name && !problem && !same)) {
            return;
          }
          rename.mutate(name, { onSuccess: onClose });
        }}
      >
        <Fields>
          <Field label="Name" error={nameError}>
            <input
              ref={autofocus}
              value={name}
              onChange={(event) => setName(toName(event.target.value))}
              spellCheck={false}
              autoComplete="off"
              aria-invalid={!!nameError}
              className={`${input} ${invalid(nameError)}`}
              disabled={rename.isPending}
            />
          </Field>
        </Fields>
        <p className="mt-2 text-xs text-sec">
          Environments, apps and the link stay. Scripts that name the server
          need updating.
        </p>
        <Footer
          submit="Rename server"
          pending={rename.isPending}
          problem={v.problem([!name && "a name"], !!problem || same)}
          error={rename.error}
          cli={[`dock server rename ${server.name} ${name || "<name>"}`]}
          onClose={onClose}
        />
      </form>
    </Dialog>
  );
}
