import { queryOptions } from "@tanstack/react-query";
import { api, body } from "../../api/client";
import { useAction } from "../../data/mutations";
import { keys } from "../../data/queries";

// The platform's sign-in policy (ADR 0049, `dock security`): whether
// operators run the platform only after signing in with a second factor.

const securityKey = ["platform-security"];

export const platformSecurityQuery = () =>
  queryOptions({
    queryKey: securityKey,
    queryFn: () => body(api.GET("/api/v1/platform/security")),
  });

/**
 * Turning it on needs the caller's own sign-in to have used a second
 * factor; operators without one lose the console until they add one.
 */
export const useSetPlatformSecurity = () =>
  useAction(
    (operators_two_factor: boolean) =>
      body(
        api.PUT("/api/v1/platform/security", {
          body: { operators_two_factor },
        }),
      ),
    () => [securityKey, keys.users, ["audit"]],
  );
