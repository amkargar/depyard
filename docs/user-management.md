# User Management, Authentication & Access Tokens

This document describes how accounts, sessions and access tokens work in
Mirror Registry, and how to operate them.

## Roles

There are exactly two roles:

| Role | What it can do |
|---|---|
| `admin` | Everything: manage registries, users, tokens, settings, audit logs, and the base-storage configuration. |
| `user` | Sign in, browse public registries, manage their own profile, and manage their own personal access tokens. |

There is no RBAC framework. Registry-specific granularity lives in the access
token model (see below), not in the role model.

## Signing in

`POST /api/admin/auth/login` accepts a username and password and returns a
session token. The token is also set as the `mr_session` cookie, which is
`HttpOnly` and `SameSite=Lax`.

The session cookie carries the `Secure` attribute whenever the request reached
the server over TLS — directly, or through a proxy that terminated TLS
(`X-Forwarded-Proto: https`). Plain HTTP on loopback (`localhost`, `127.0.0.1`,
`::1`) is the single exception, so that local development over plain HTTP keeps
working. Everything else is marked `Secure`.

Users can sign in with either a session cookie or an `Authorization: Bearer
<token>` header. The `Authorization: Basic` header is also supported for package
manager protocols; see [Authentication for package managers](#authentication-for-package-managers).

## The first administrator

On first boot the server creates an administrator account. The credentials are:

- `ADMIN_USERNAME` — defaults to `admin`.
- `ADMIN_PASSWORD` — **no default**. When the variable is not set, the server
  generates a strong random password and prints it once in the startup log:

  ```
  WARN no ADMIN_PASSWORD configured: a password was generated for the initial admin account
       username=admin password=<generated> note="this password is shown only once ..."
  ```

  Store it and change it after signing in. Setting `ADMIN_PASSWORD` explicitly
  restores the deterministic behaviour.

The bootstrap administrator is created whenever **no active administrator
exists**, not merely when the user table is empty. If the only administrator is
removed, the next start creates a new one.

## Password policy

Passwords must be at least **12 characters**. The floor is configurable with the
`MIN_PASSWORD_LENGTH` environment variable for deployments that need to relax
it (for example, when migrating an existing corpus of short passwords):

```bash
MIN_PASSWORD_LENGTH=8
```

Invalid values fall back to the safe default of 12.

## Administrator lockout protection

The installation cannot be locked out of its own admin console:

- The last **active** administrator cannot be demoted to `user`.
- The last **active** administrator cannot be disabled.
- The last **active** administrator cannot be deleted.

These requests are refused with `409 Conflict` and the message
`cannot remove the last active administrator`.

## Sessions and credential changes

Changing a user's password, role, or disabled state invalidates that user's
sessions immediately. Changing the password additionally revokes all of that
user's personal access tokens, because a token is an alternative credential
that would otherwise outlive the password it was issued alongside.

Disabling an account deletes its sessions. Deleting an account deletes its
sessions and revokes its tokens.

## Self-service account management

Any signed-in user can manage their own account. These endpoints require
authentication but not administrator rights.

### `GET /api/admin/profile`

Returns the caller's own account and the effective password policy:

```json
{
  "user": { "id": "…", "username": "alice", "email": "alice@example.com", "role": "user", "disabled": false },
  "is_admin": false,
  "min_password_length": 12
}
```

### `PUT /api/admin/profile`

Updates the caller's own username and email:

```json
{ "username": "alice2", "email": "alice2@example.com" }
```

Role and disabled state are **not** editable here — those are administrative
decisions, and allowing self-service changes would let a user grant themselves
administrator rights. A username that is already taken returns `409 Conflict`.

### `POST /api/admin/profile/password`

Changes the caller's own password. The current password must be supplied.

```json
{ "current_password": "…", "new_password": "…" }
```

A wrong current password returns `401`; a new password below the policy
minimum returns `400`. On success every other session and every personal token
of that user is revoked.

## Personal access tokens

Personal tokens let a user authenticate a script or CI job without sharing
their password.

### `GET /api/admin/my/tokens`

Lists the tokens owned by the caller. A user only ever sees their own tokens.

### `POST /api/admin/my/tokens`

Creates a token owned by the caller:

```json
{
  "name": "my-ci",
  "registry_id": "",
  "scopes": ["read", "publish"],
  "namespaces": ["my_package", "my_org.*"],
  "expires_days": 90
}
```

The response contains the plaintext token **once**:

```json
{ "token": { "id": "…", "prefix": "mr_abc12345", "scopes": ["read", "publish"] },
  "plaintext": "mr_…" }
```

### `POST /api/admin/my/tokens/{id}/revoke` and `DELETE /api/admin/my/tokens/{id}`

Revoke or delete one of the caller's own tokens. Acting on somebody else's
token returns `403 Forbidden`.

## Token permissions never exceed the owner's

A token can never grant more than the user who owns it:

- A regular user cannot mint an `admin`-scoped token. The request is refused
  with `403` and the message
  `invalid permission scope: token scope exceeds the owner's permissions`.
- A token issued while its owner was an administrator loses its administrative
  reach as soon as the owner is demoted, even though the token itself is still
  valid.
- A token whose owner account has been deleted no longer authenticates; the
  token is revoked automatically the first time it is presented.

Administrators can still create tokens for any registry through the
administrative API (`GET|POST /api/admin/tokens`, and the matching
`revoke`/`DELETE` routes). Those endpoints remain administrator-only.

## Registry isolation

A token is additionally bound to a registry instance, a registry type, and
optionally a set of package namespaces:

| Field | Effect |
|---|---|
| `registry_id` / `registry_slug` | The token only works against that registry instance. Empty means every registry. |
| `registry_type` | The token only works against that ecosystem (for example `pypi`). |
| `namespaces` | Package-name patterns the token may touch. Empty means every package. Patterns support `*` wildcards. |

## Authentication for package managers

`pip`, `twine`, `npm`, `go` and `flutter` clients do not support bearer
headers, so the platform accepts an `Authorization: Basic` header whose
username is the account and whose password is the **token**:

```
Authorization: Basic base64(<username>:<token>)
```

The username is recorded for audit purposes; the token is what actually
authenticates. Sessions, bearer tokens and Basic-auth tokens all resolve to
the same permission model.

## Administrator APIs

These remain administrator-only and return `403 Forbidden` for regular users:

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/admin/users` | List all accounts |
| `POST` | `/api/admin/users` | Create an account |
| `PUT` | `/api/admin/users/{id}` | Edit username, email, role, status, or reset the password |
| `DELETE` | `/api/admin/users/{id}` | Delete an account and revoke its credentials |
| `GET` | `/api/admin/tokens` | List every token |
| `POST` | `/api/admin/tokens` | Create a token for any account |
| `POST` | `/api/admin/tokens/{id}/revoke` | Revoke a token |
| `DELETE` | `/api/admin/tokens/{id}` | Delete a token |

Editing a user through `PUT /api/admin/users/{id}`:

- Omitting `password` keeps the current password.
- Supplying a password resets it and invalidates that user's sessions and
  tokens.
- Changing `role` or `disabled` invalidates that user's sessions.
- Changing `username` to a name that is already taken returns `409 Conflict`.

## Configuration reference

| Variable | Default | Meaning |
|---|---|---|
| `ADMIN_USERNAME` | `admin` | Username of the bootstrapped administrator. |
| `ADMIN_PASSWORD` | *(generated)* | Password of the bootstrapped administrator. |
| `MIN_PASSWORD_LENGTH` | `12` | Minimum accepted password length. |
| `SESSION_TTL` | `24h` | How long a session cookie stays valid. |

## Audit trail

Every authentication and account event is written to the audit log with the
actor, the action, the source address and the outcome:

| Action | Recorded when |
|---|---|
| `auth.login` | A sign-in attempt, successful or not. |
| `profile.update` | A user edits their own profile. |
| `profile.password_change` | A user changes their own password. |
| `user.create`, `user.update`, `user.delete` | An administrator manages accounts. |
| `token.create`, `token.revoke`, `token.delete` | A token is created, revoked or deleted. |
