import { createFileRoute } from "@tanstack/react-router";
import { AccountPage } from "../features/account/AccountPage";

// The signed-in person's own account: who they are and their password.
export const Route = createFileRoute("/_app/account")({
  component: AccountPage,
});
