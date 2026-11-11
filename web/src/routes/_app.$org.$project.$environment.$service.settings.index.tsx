import { createFileRoute } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { SettingsPanel } from "../features/settings/SettingsPanel";
import {
  isTextSettingKey,
  type TextSettingKey,
} from "../features/settings/settings";

// Settings' Build & run view (ADR 0059, ADR 0076): source, builder,
// commands, health checks, limits, volumes and server. ?edit=port opens
// that setting's editor, for links that point at a fix (a failed release).
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/settings/",
)({
  validateSearch: (s: Record<string, unknown>): { edit?: TextSettingKey } =>
    typeof s.edit === "string" && isTextSettingKey(s.edit)
      ? { edit: s.edit }
      : {},
  component: BuildAndRun,
});

function BuildAndRun() {
  return (
    <SettingsPanel
      serviceRef={servicePath(Route.useParams())}
      edit={Route.useSearch().edit}
    />
  );
}
