import { lazy, Suspense, useState } from "react";
import { useCanDeploy, useEnvironment, useService } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { Button } from "../../ui/kit";
import { restoredFrom } from "./points";

const SwitchDialog = lazy(() => import("./SwitchDialog"));

/**
 * On a restored database's own page (ADR 0061): the service it came from,
 * Use (references move here) and Switch back (they move to the origin).
 * Each opens the switch dialog, whose dry run says what would change.
 * Nothing shows for other services.
 */
export function RestoredFrom({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const { service } = useService(ref);
  const { services } = useEnvironment(ref);
  const mayDeploy = useCanDeploy(ref);
  const [switching, setSwitching] = useState<"use" | "back" | null>(null);
  const origin = restoredFrom(
    ref.service,
    service.template,
    services.map((s) => s.service),
  );
  if (!origin || !mayDeploy) return null;
  return (
    <p className="flex flex-wrap items-center gap-2">
      <span className="text-mut">
        Restored from <span className="font-mono text-fg">{origin}</span>.
      </span>
      <Button small onClick={() => setSwitching("use")}>
        Use {ref.service}
      </Button>
      <Button small onClick={() => setSwitching("back")}>
        Switch back to {origin}
      </Button>
      <Suspense fallback={null}>
        {switching && (
          <SwitchDialog
            serviceRef={switching === "use" ? { ...ref, service: origin } : ref}
            to={switching === "use" ? ref.service : origin}
            onClose={() => setSwitching(null)}
          />
        )}
      </Suspense>
    </p>
  );
}
