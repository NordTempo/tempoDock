import { useNavigate } from "@tanstack/react-router";
import { useRestart } from "../data/mutations";
import { useReleases, useService } from "../data/queries";
import { RollbackConfirm } from "../features/service/ReleasesTable";
import { Confirm } from "../ui/kit";
import type { ConfirmRequest } from "./confirmHost";
import { useOrg } from "./location";

/**
 * The confirm a palette action opens (confirmHost): the rollback one the
 * releases use, or the restart one the service header uses. Loaded with
 * the first such action.
 */
export default function ActionConfirm({
  request,
  onClose,
}: {
  request: ConfirmRequest;
  onClose: () => void;
}) {
  return request.kind === "restart" ? (
    <RestartConfirm request={request} onClose={onClose} />
  ) : (
    <Rollback request={request} onClose={onClose} />
  );
}

function Rollback({
  request: { ref, to },
  onClose,
}: {
  request: Extract<ConfirmRequest, { kind: "rollback" }>;
  onClose: () => void;
}) {
  const org = useOrg();
  const navigate = useNavigate();
  const releases = useReleases(ref);
  const target = releases.find((r) => r.number === to);
  const serving = releases.find((r) => r.status === "active");
  if (!target) return null;
  return (
    <RollbackConfirm
      serviceRef={ref}
      target={target}
      serving={serving}
      onDone={() => {
        onClose();
        void navigate({
          to: "/$org/$project/$environment/$service",
          params: { org, ...ref },
        });
      }}
    />
  );
}

function RestartConfirm({
  request: { ref },
  onClose,
}: {
  request: Extract<ConfirmRequest, { kind: "restart" }>;
  onClose: () => void;
}) {
  const restart = useRestart();
  const { service } = useService(ref);
  const r = `r${service.active_release}`;
  return (
    <Confirm
      title={`Restart ${ref.service} in ${ref.environment}?`}
      action={`Restart ${ref.service}`}
      cli={`dock restart -s ${ref.service} -e ${ref.environment}`}
      pending={restart.isPending}
      error={restart.error}
      onConfirm={() => restart.mutate(ref, { onSuccess: onClose })}
      onCancel={() => {
        restart.reset();
        onClose();
      }}
    >
      {service.stateful ? (
        <>
          A new release with {r}'s image and config.{" "}
          <b>
            {ref.service} is down for a few seconds while {r} stops
          </b>
          : a volume allows one writer.
        </>
      ) : (
        <>
          A new release with {r}'s image and config starts next to {r} and takes
          over once it passes its health check. {ref.service} stays reachable.
        </>
      )}
    </Confirm>
  );
}
