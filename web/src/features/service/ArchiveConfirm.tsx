import { useNavigate } from "@tanstack/react-router";
import { useArchiveService } from "../../data/mutations";
import type { ServiceRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { Confirm } from "../../ui/kit";

/**
 * Asks before archiving a service, then archives it and goes to its
 * project. The header's More menu and the end of Build & run open it.
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
      Volumes, variables and release history are kept.
    </Confirm>
  );
}
