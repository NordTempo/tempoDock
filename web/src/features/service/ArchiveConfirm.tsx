import { useNavigate } from "@tanstack/react-router";
import { useArchiveService } from "../../data/mutations";
import type { ServiceRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { Confirm } from "../../ui/kit";

/**
 * Asks before archiving a service, like Stop: what happens, what is
 * kept and where it is listed, the CLI line and a red button. Then it
 * archives, goes to the project and says so (the mutation's notice).
 * The header's More menu and Build & run open it.
 */
export function ArchiveConfirm({
  serviceRef: ref,
  onCancel,
}: {
  serviceRef: ServiceRef;
  onCancel: () => void;
}) {
  const org = useOrg();
  const navigate = useNavigate();
  const archive = useArchiveService();
  return (
    <Confirm
      title={`Archive ${ref.service}?`}
      action={`Archive ${ref.service}`}
      cli={`dock service archive ${ref.service} -e ${ref.environment} -p ${ref.project}`}
      danger
      pending={archive.isPending}
      error={archive.error}
      onConfirm={() =>
        archive.mutate(ref, {
          onSuccess: () =>
            navigate({
              to: "/$org/$project",
              params: { org, project: ref.project },
            }),
        })
      }
      onCancel={() => {
        archive.reset();
        onCancel();
      }}
    >
      <b>
        Its container stops and its domains stop answering in {ref.environment}.
      </b>{" "}
      Volumes, variables and release history are kept, listed under Archived on
      the project page. It can't be started again or rolled back, and its images
      leave the rollback window.
    </Confirm>
  );
}
