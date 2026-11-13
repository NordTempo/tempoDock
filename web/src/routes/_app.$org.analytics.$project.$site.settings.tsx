import { createFileRoute } from "@tanstack/react-router";
import { SettingsPage } from "../features/analytics/SettingsPage";

// A site's settings, the last of its views (Saile's pick, 2026-11-13):
// /nordtempo/analytics/shop/shop.example.com/settings.
export const Route = createFileRoute(
  "/_app/$org/analytics/$project/$site/settings",
)({
  component: SettingsPage,
});
