import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { api, body } from "../../api/client";
import { useAction } from "../../data/mutations";
import { forgetEnvironment, keys, useOrgAdmin } from "../../data/queries";
import type { Environment } from "../../data/types";
import { useOrg } from "../../shell/location";
import { Button, Confirm } from "../../ui/kit";
import { notify } from "../../ui/notice";

/**
 * `dock environment delete` (ADR 0103), for the environment's Delete row
 * on its Settings tab: deletes it and everything in it, its name typed
 * again in the dialog. Its cached queries are dropped once it's gone
 * (forgetEnvironment): refetching them answers 404. The overview refetches.
 */
const useDeleteEnvironment = (e: Environment) =>
  useAction(
    () =>
      body(
        api.DELETE("/api/v1/projects/{project}/environments/{environment}", {
          params: { path: { project: e.project, environment: e.name } },
          body: { confirm: e.name, id: e.id },
        }),
      ),
    () => [keys.overview(e.project)],
  );

/**
 * The Delete row's content: why it can't be deleted, or the button and
 * its typed confirmation. only is whether it is the project's only
 * environment, which the server refuses too.
 */
export function DeleteEnvironment({
  environment: e,
  only,
}: {
  environment: Environment;
  only: boolean;
}) {
  const allowed = useOrgAdmin();
  const org = useOrg();
  const navigate = useNavigate();
  const client = useQueryClient();
  const remove = useDeleteEnvironment(e);
  const [confirming, setConfirming] = useState(false);
  if (!allowed) {
    return (
      <p className="text-mut">
        Only an admin of the whole organization can delete {e.name}.
      </p>
    );
  }
  if (only) {
    return (
      <p className="text-mut">
        {e.name} is the project's only environment. Create another first, or
        archive the project on Project settings.
      </p>
    );
  }
  const cli = `dock environment delete ${e.name} -p ${e.project} --confirm ${e.name}`;
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
      <span className="min-w-0 flex-1 text-mut">
        deletes its services, volumes and history; can't be undone
      </span>
      <Button danger onClick={() => setConfirming(true)}>
        Delete {e.name}
      </Button>
      {confirming && (
        <Confirm
          danger
          action={`Delete ${e.name}`}
          title={`Delete ${e.name} permanently?`}
          typed={e.name}
          cli={cli}
          pending={remove.isPending}
          error={remove.error}
          onConfirm={() =>
            remove.mutate(undefined, {
              onSuccess: async (done) => {
                setConfirming(false);
                notify(`Deleted ${done.environment}`);
                for (const note of done.notes ?? []) notify(note, "info");
                await navigate({
                  to: "/$org/$project/settings",
                  params: { org, project: e.project },
                  search: { env: undefined },
                });
                forgetEnvironment(client, {
                  project: e.project,
                  environment: e.name,
                });
              },
            })
          }
          onCancel={() => {
            // A request still running keeps its pending state, so the
            // dialog opened again can't send a second one.
            if (!remove.isPending) remove.reset();
            setConfirming(false);
          }}
        >
          Its services are stopped, and their volumes, containers, releases,
          variables, domains, logs and analytics are deleted from its server and
          from Dock. Closed previews cloned from it go too. The name{" "}
          <b>{e.name}</b> is free again. Its backups stay in their repository.
          Its activity stays. This can't be undone.
        </Confirm>
      )}
    </div>
  );
}
