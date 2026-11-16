# 0038: Tokens in organization settings, your own on your account

Status: accepted (asked by Saile, 2026-10-30); superseded in part by [0080](0080-permissions-roles-groups-and-agents.md)
(an org's tokens are org agents, managed with `agents.manage` and capped at
what their creator holds; `dock login` tokens are personal agents).

## Context

Tokens had two pages. Organization admins used `/<org>/tokens`, and
operators were redirected to `/console/tokens`. Both listed the same
thing: the current organization's tokens plus its members' `dock login`
tokens. Tokens belong to an organization, so a page in the platform
console looked platform-wide when it wasn't. Members who weren't admins
had no way to see or revoke even their own `dock login` tokens from the UI.

## Decision

| Topic | Decision |
|---|---|
| Organization's tokens | One page, Settings › Tokens (`/<org>/settings/tokens`), for admins, operators included. Operator tokens are marked there. The console page is gone |
| Your tokens | Your account page lists your `dock login` tokens and the tokens you created in the organization (`GET /me/tokens`, `dock token list --mine`) |
| Revoking your own | `DELETE /tokens/{id}` (`dock token revoke`) also accepts the token's owner, with any role. Revoking only takes power away |
| Creator | A manual token records its creator in `tokens.user_id`, backfilled from `created_by` (migration 0031). It still acts for the organization, not for that user: only `dock login` tokens act for their user, and disabling the creator leaves the token working |
| Creating | Still admins only. Letting members create tokens capped at their role is a separate decision |

## Consequences

- CI and agent tokens stay organization credentials, so they survive
  their creator leaving. Admins still see and revoke every one of them.
- `tokens.user_id` means "acts for" on `dock login` tokens and "created
  by" on manual tokens. Code that acts for a token's user checks
  `origin = 'cli_login'` first, as authentication and
  `RevokeUserLoginTokens` already do.
