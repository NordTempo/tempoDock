import { useState } from "react";
import { useRestoreBackup } from "../../data/mutations";
import { useEnvironment } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { Dialog } from "../../ui/Dialog";
import { ago, clock, day, size } from "../../ui/format";
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
import { useRestoreSnapshot } from "./data";
import { isEmpty, pointName, type RestorePoint, restoredName } from "./points";

/**
 * The one restore dialog (ADR 0061), for a backup or a snapshot alike. It
 * names the point by kind, age and size, and warns when the point is
 * empty (failed ones never get here). A template database restores
 * into a new service (the name is editable) and the live one is not
 * touched; any other service restores a snapshot in place, with a safety
 * snapshot first. Loaded when it opens.
 */
export default function RestoreDialog({
  serviceRef: ref,
  point,
  intoNew,
  onClose,
  onStarted,
}: {
  serviceRef: ServiceRef;
  point: RestorePoint;
  /** A template database: restores go into a new service. */
  intoNew: boolean;
  onClose: () => void;
  onStarted: () => void;
}) {
  const taken = useEnvironment(ref).services.map((s) => s.service.name);
  const [name, setName] = useState(() => restoredName(ref.service, taken));
  const backup = useRestoreBackup();
  const snapshot = useRestoreSnapshot();
  const pending = backup.isPending || snapshot.isPending;
  const v = useValidation();
  const as = name.trim();
  const nameError = intoNew
    ? (nameProblem("service", as, taken) ??
      v.required(!as, "Enter a name for the new service."))
    : undefined;
  const flags = `-s ${ref.service} -e ${ref.environment}`;
  const cli =
    point.kind === "backup"
      ? `dock backup restore ${ref.service} --snapshot ${point.run.snapshot?.slice(0, 8)} --as ${as || "<name>"} -e ${ref.environment}`
      : `dock volume snapshot restore ${point.snapshot.id} ${flags}${intoNew ? ` --as ${as || "<name>"}` : " --in-place"}`;

  const submit = () => {
    if (!v.check(!intoNew || (!!as && !nameError))) return;
    const done = { onSuccess: onStarted };
    if (point.kind === "backup" && point.run.snapshot) {
      backup.mutate({ ref, snapshot: point.run.snapshot, as }, done);
    } else if (point.kind === "snapshot") {
      snapshot.mutate(
        intoNew
          ? { ref, id: point.snapshot.id, as }
          : { ref, id: point.snapshot.id, in_place: true },
        done,
      );
    }
  };

  return (
    <Dialog
      title={
        intoNew
          ? `Restore ${pointName(point)} into a new service?`
          : `Restore ${ref.service} from ${pointName(point)}?`
      }
      onClose={onClose}
    >
      <form
        ref={v.form}
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <p className="text-sec">
          The {point.kind} from {ago(point.at)} ({day(point.at)}{" "}
          {clock(point.at)}), {isEmpty(point) ? "empty" : size(point.size)},{" "}
          {point.server ? `server ${point.server}` : point.where}
          {point.kind === "snapshot" && point.snapshot.note
            ? `: ${point.snapshot.note}`
            : ""}
          .
        </p>
        {isEmpty(point) && (
          <p className="text-warn">
            This {point.kind} holds no data (0 B):{" "}
            {intoNew
              ? "the new service would start empty."
              : `${ref.service} would be left empty.`}{" "}
            Pick a point with a size instead.
          </p>
        )}
        {intoNew ? (
          <>
            {/* Two steps, said before the first: nothing live changes yet. */}
            <ol className="flex list-decimal flex-col gap-1 pl-5 text-sec">
              <li>
                This restores into a new service; the live {ref.service} is not
                touched.{" "}
                {point.kind === "backup"
                  ? "The new service gets its own credentials."
                  : "The new service keeps the snapshot's credentials."}
              </li>
              <li>
                Once the data checks out,{" "}
                <b className="text-fg">Use {as || "it"}</b> switches the
                services that use {ref.service} over to it (and back, if
                needed).
              </li>
            </ol>
            <Fields>
              <Field label="New service" error={nameError}>
                <input
                  ref={autofocus}
                  value={name}
                  aria-invalid={!!nameError}
                  onChange={(e) => setName(toName(e.target.value))}
                  className={`${input} ${invalid(nameError)}`}
                  autoComplete="off"
                  spellCheck={false}
                />
              </Field>
            </Fields>
          </>
        ) : (
          <p className="text-sec">
            In place: {ref.service} stops, a safety snapshot keeps what it holds
            now (restoring that one undoes this), the snapshot is copied in, and
            it starts again.
          </p>
        )}
        <Footer
          className="mt-1"
          submit={
            intoNew ? `Restore into ${as || "new service"}` : "Restore in place"
          }
          pending={pending}
          problem={v.problem([intoNew && !as && "a name"], !!nameError)}
          error={backup.error ?? snapshot.error}
          cli={[cli]}
          onClose={onClose}
        />
      </form>
    </Dialog>
  );
}
