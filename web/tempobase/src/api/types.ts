import type { components, operations } from "./schema";

// tempoBase's API types, from its generated schema (web/tempobase/openapi.json,
// `pnpm gen:api`). The data and policy types are Dock's apitypes; the
// contract test keeps them identical in both specs, so the shared panel's
// types (src/features/data/types.ts) fit these.

type Schemas = components["schemas"];

export type AuditDetail = Schemas["AuditDetail"];
export type AuditEvent = Schemas["AuditEvent"];
export type Connection = Schemas["Connection"];
export type ConnectionTest = Schemas["TestConnectionResult"];
export type CreateConnectionRequest = Schemas["CreateConnectionRequest"];
export type CreatedToken = Schemas["CreatedToken"];
export type CreateInviteRequest = Schemas["CreateInviteRequest"];
export type CreateTokenRequest = Schemas["CreateTokenRequest"];
/** An invite or a password reset; `url` only when just created. */
export type Link = Schemas["Link"];
/** GET /links/{code}: what a link is for, before using it. */
export type LinkInfo = Schemas["LinkInfo"];
/** GET /me: the caller, a signed-in person or a token. */
export type Me = Schemas["Me"];
/** GET /data-policy, GET /connections/{c}/data-policy. */
export type Policy = Schemas["DataPolicy"];
export type SetupRequest = Schemas["SetupRequest"];
export type TestConnectionRequest = Schemas["TestConnectionRequest"];
export type Token = Schemas["Token"];
export type UpdateConnectionRequest = Schemas["UpdateConnectionRequest"];
export type UpdateTokenRequest = Schemas["UpdateTokenRequest"];
export type UpdateUserRequest = Schemas["UpdateUserRequest"];
export type UseLinkRequest = Schemas["UseLinkRequest"];
export type User = Schemas["User"];

export type Role = Me["role"];
export type TlsMode = Connection["tls"];
/** Where a policy rule comes from (tempoBase sends built_in, default, connection). */
export type PolicyScope = NonNullable<Policy["effective"]>[number]["from"] &
  string;
/** GET /audit's filters. */
export type AuditQuery = NonNullable<
  operations["list-audit"]["parameters"]["query"]
>;
