import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { keys } from "../../data/queries";
import type { ServiceRef } from "../../data/types";

// Who can open a service's domains (ADR 0030), for the Domains tab.

type Schemas = components["schemas"];
export type Access = Schemas["Access"];
export type AccessMode = Access["mode"];
export type SetAccess = Schemas["SetAccessRequest"];

const accessPath =
  "/api/v1/projects/{project}/environments/{environment}/services/{service}/access";

export const useAccess = (ref: ServiceRef) =>
  useQuery({
    queryKey: keys.access(ref),
    queryFn: () => body(api.GET(accessPath, { params: { path: ref } })),
  });

/** Changes the access; the answer replaces the cached one. */
export function useSetAccess(ref: ServiceRef) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (req: SetAccess) =>
      body(api.PUT(accessPath, { params: { path: ref }, body: req })),
    onSuccess: (a) => client.setQueryData(keys.access(ref), a),
  });
}

/** "Open to anyone", as the closed line and `dock access` say it. */
export function accessText(a: Access) {
  switch (a.mode) {
    case "password":
      return `Password for ${a.username}`;
    case "login":
      return "Dock login";
    default:
      return "Open to anyone";
  }
}

/** The `dock access` command that sets req. */
export function accessCli(ref: ServiceRef, req: SetAccess) {
  const user =
    req.mode === "password" ? ` --username ${req.username || "<name>"}` : "";
  return `dock access ${req.mode}${user} -s ${ref.service} -e ${ref.environment}`;
}
