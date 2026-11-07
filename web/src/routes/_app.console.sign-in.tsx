import { createFileRoute } from "@tanstack/react-router";
import { SignInPage } from "../features/console/SignInPage";
import {
  signInSettingsQuery,
  ssoProvidersQuery,
} from "../features/console/signIn";

// How people sign in to this Dock (ADR 0065): identity providers,
// password sign-in and invite only. Operators only.
export const Route = createFileRoute("/_app/console/sign-in")({
  loader: ({ context: { queryClient } }) =>
    Promise.all([
      queryClient.ensureQueryData(ssoProvidersQuery()),
      queryClient.ensureQueryData(signInSettingsQuery()),
    ]),
  component: SignInPage,
});
