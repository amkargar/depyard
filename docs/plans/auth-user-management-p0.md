# Plan: P0 Authentication, User Management & Security Fixes

Addresses the P0 findings of the project review. Scope is deliberately limited to
confirmed defects plus the missing user-management surface — no new registry
ecosystems, no new infrastructure dependencies.

## Confirmed defects being fixed

| # | Defect | Location |
|---|---|---|
| 1 | `refresh()` returns admin status instead of authentication state | `frontend/composables/useAuth.ts` |
| 2 | Login always redirects to `/admin`; admin layout bounces non-admins to `/login` — normal users can never use the app | `frontend/pages/login.vue`, `frontend/layouts/admin.vue` |
| 3 | Deleted users' access tokens keep working | `auth.Service.authenticateAccessToken` |
| 4 | Deleting a user does not revoke their tokens | `auth.Service.DeleteUser` |
| 5 | Password change does not invalidate existing sessions | `auth.Service.UpdateUser` |
| 6 | Demoting/disabling the last active admin is allowed (lockout) | `auth.Service.UpdateUser` |
| 7 | `EnsureBootstrapAdmin` checks "any user exists" instead of "an admin exists" | `auth.Service.EnsureBootstrapAdmin` |
| 8 | Session cookie lacks `Secure` | `admin.Handler.handleLogin`, `handleLogout` |
| 9 | Minimum password length is 6 characters | `auth.Service.CreateUser`, `UpdateUser` |
| 10 | Default admin password is a known constant (`admin123`) | `internal/config/config.go` |
| 11 | All token endpoints are admin-only; users have no personal tokens | `admin.Handler` |
| 12 | Username cannot be edited | `auth.Service.UpdateUser` |
| 13 | No self-service password change / profile endpoint | `admin.Handler` |

## Design decisions

### Authentication vs. authorization separation

`useAuth().refresh()` will return `authenticated` (a boolean), and the admin layout
will gate on `isAdmin` rather than on authentication. A new `dashboard` layout
serves non-admin users. Login redirects to a role-aware destination:

- admin → `/admin`
- user → `/dashboard`

### Roles

Keep exactly two roles (`admin`, `user`), as the review recommends. No RBAC
framework is introduced. Personal tokens inherit the owner's role: a `user` can
only mint tokens with `read` scope; an `admin` may mint any scope.

### Token ownership

New self-service endpoints live under `/api/admin/profile/...` and
`/api/admin/my-tokens` but are guarded by a new `requireAuth` middleware
(any authenticated principal), not `requireAdmin`. Every token mutation checks
ownership: a non-admin may only act on tokens they created.

To make ownership checks possible the token store gains
`ListTokensByUser(ctx, userID)`, and `ListTokens` gains an owner filter.

### Last-admin protection

A helper `countActiveAdmins(ctx)` backs two guards:

- `UpdateUser` refuses to demote or disable the last active admin.
- `DeleteUser` refuses to delete the last active admin.

`EnsureBootstrapAdmin` creates the bootstrap admin when **no active admin
exists**, not merely when the user table is empty.

### Password policy

Minimum length raised from 6 to 12 characters. A `PasswordPolicy` knob in the
config keeps it configurable for existing deployments that need to relax it.

### Session invalidation

Any change to a user's password, role, or disabled state deletes that user's
sessions. Any change to the password also revokes the user's personal tokens,
since a token is an alternative credential.

### Secure cookie

The session cookie gains `Secure` unless the request is plain HTTP on loopback,
so local development over `http://localhost` keeps working while real
deployments are protected. This is detected from the request rather than
configured, to avoid a config knob that operators get wrong.

### Default admin password

The hardcoded `admin123` default is replaced by a **randomly generated**
password that is logged once at first boot, or by an explicit `ADMIN_PASSWORD`
environment variable. Operators who set the variable get the deterministic
behaviour they expect; everyone else gets a safe random credential.

## Implementation phases

1. **Auth store** — add `ListTokensByUser`, `CountActiveAdmins` to the `Store`
   interface plus the memory and MongoDB implementations.
2. **Auth service** — token ownership checks, deleted-user token invalidation,
   session invalidation on credential change, last-admin guards, username
   editing, password policy, `EnsureBootstrapAdmin` fix.
3. **Admin handler** — `requireAuth` middleware, self-service profile and
   password endpoints, personal token endpoints with ownership checks, `Secure`
   cookie.
4. **Frontend** — fix `refresh()`, role-aware login redirect, new dashboard
   layout and pages for non-admin users, personal token management UI, profile
   page, admin user editing UI.
5. **Config** — `ADMIN_PASSWORD` handling, password policy knob.
6. **Tests** — backend unit/integration tests for every fixed defect;
   frontend typecheck + static generate.
7. **Docs** — README and a dedicated user-management document.

## Implementation status

All thirteen defects are fixed and covered by tests.

| Phase | Status | Notes |
|---|---|---|
| 1. Auth store | Done | `CountActiveAdmins`, `ListTokensByUser`, `RevokeTokensByUser` added to the `Store` interface with memory and MongoDB implementations. |
| 2. Auth service | Done | Token ownership, deleted-user invalidation, session invalidation on password/role/status change, last-admin guards, username editing, password policy, bootstrap-admin fix. |
| 3. Admin handler | Done | `requireAuth` middleware, `/api/admin/profile` (GET/PUT), `/api/admin/profile/password`, `/api/admin/my/tokens` (GET/POST/revoke/DELETE), `Secure` session cookies. |
| 4. Frontend | Done | `refresh()` fix, role-aware login redirect, new `user` layout with `/dashboard`, `/profile` and `/tokens` pages, admin user-edit dialog. |
| 5. Config | Done | `ADMIN_PASSWORD` has no default (generated once and logged), `MIN_PASSWORD_LENGTH` defaults to 12. |
| 6. Tests | Done | `backend/internal/auth/auth_p0_test.go`, `backend/internal/auth/password_test.go`, `backend/internal/config/config_test.go`, `backend/internal/server/auth_integration_test.go`. |
| 7. Docs | Done | `docs/user-management.md` plus README updates. |

### Deviations from the plan

- **Endpoint paths.** The self-service endpoints are `/api/admin/profile*` and
  `/api/admin/my/tokens*` rather than `/api/admin/my-tokens`. The
  `/api/admin/my/...` prefix makes the ownership scope obvious in the URL and
  keeps the personal-token routes grouped together.
- **Mixed-scope token requests are refused, not narrowed.** A regular user
  requesting `[read, admin]` gets an error instead of a silently narrowed
  `[read]` token. Silently dropping the admin scope would hide the mistake; the
  frontend already only offers the scopes a user actually holds.
- **The admin user-update response changed shape.** `PUT /api/admin/users/{id}`
  now returns `{"user": {...}}` instead of the bare user object, matching the
  profile endpoint.

## Explicitly out of scope for this change

- The first-run setup wizard (P1) — it depends on the admin-account work here,
  but it is a separate, larger feature.
- Offline release bundles and the `mirror-registry` CLI (P1).
- The diagnostics page (P2).
- Registry-specific access grants — the review calls for them under P2/P3;
  the token scope model already provides the isolation primitive.
