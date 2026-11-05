import { createFileRoute } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { SettingsPanel } from "../features/settings/SettingsPanel";
import {
  isTextSettingKey,
  type TextSettingKey,
} from "../features/settings/settings";

// A service's Build & run tab (ADR 0059): source, builder, commands,
// health check, resources, replicas and server. ?edit=port opens that
// setting's editor, for links that point at the fix (a failed release).
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/build",
)({
  validateSearch: (s: Record<string, unknown>): { edit?: TextSettingKey } =>
    typeof s.edit === "string" && isTextSettingKey(s.edit)
      ? { edit: s.edit }
      : {},
  component: BuildTab,
});

function BuildTab() {
  return (
    <SettingsPanel
      serviceRef={servicePath(Route.useParams())}
      edit={Route.useSearch().edit}
    />
  );
}
