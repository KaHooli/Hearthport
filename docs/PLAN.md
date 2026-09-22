# Hearthport — Design & Implementation Plan

## 1. Goals

| # | Requirement | How it is met |
|---|---|---|
| G1 | Self-hosted web portal in a single Docker container | One static Go binary with embedded assets; distroless image; state in a `/data` volume |
| G2 | Login page shows basic public information | Admin-editable title, logo, Markdown message and links, rendered on the unauthenticated login page |
| G3 | Sign-in via OIDC against Authentik | Authorization Code flow with PKCE, `state` and `nonce` |
| G4 | Users see only the apps they can access in Authentik | Authentik's API is asked which applications the signed-in user can access (§5) |
| G5 | First run: `admin` user with a password printed to stdout | A random password is generated on first boot and printed once in a banner in the container logs (§4.1) |
| G6 | OIDC is configured in the web UI by admins | Admin → Authentication settings page, with a "Test & confirm" step (§4.2) |
| G7 | After OIDC is confirmed, OIDC is the only way to log in | The local login form and endpoint are disabled when a confirmed OIDC config exists… |
| G8 | …unless an env var re-enables username/password | …or when `HEARTHPORT_ENABLE_LOCAL_LOGIN=true` is set (§4.3) |

### Non-goals (v1)
- Acting as a reverse proxy or forward-auth provider (Authentik's outposts already do this).
- Identity providers other than Authentik. The OIDC layer stays generic, but app discovery is Authentik-specific.
- Multiple local users. Only the single bootstrap `admin` account exists locally.

## 2. Technology choices

| Concern | Choice | Rationale |
|---|---|---|
| Backend | **Go** (stdlib `net/http` + `chi` router) | Single static binary, small image, strong OIDC libraries |
| OIDC | `github.com/coreos/go-oidc/v3` + `golang.org/x/oauth2` | Well maintained; handles discovery, JWKS and ID-token verification |
| Storage | **SQLite** (`modernc.org/sqlite`, pure Go, no CGO) | No separate database container; one file on the `/data` volume |
| Migrations | `pressly/goose` (embedded SQL files) | Versioned schema upgrades on startup |
| UI | Server-rendered `html/template` + **htmx** + a small CSS layer (e.g. Pico.css) | No Node build step at runtime and little JavaScript; admin forms are simple |
| Password hashing | argon2id (`golang.org/x/crypto/argon2`) | Current best practice |
| Secret encryption at rest | AES-256-GCM | Protects the OIDC client secret and Authentik API token in the DB |
| Container | Multi-stage build → `gcr.io/distroless/static:nonroot` | Small image with little attack surface; runs as a non-root user |

A SPA (Svelte or React) is an alternative if the dashboard later needs rich
interactivity. The API is designed so one could be added without changing the
backend.

## 3. Architecture

```
            ┌──────────────────────── Docker container ────────────────────────┐
 Browser ──▶│  HTTP server (:8080)                                              │
            │   ├─ Public: /login, /auth/*, /static/*, /healthz                 │
            │   ├─ User:   / (dashboard), /logout                               │
            │   └─ Admin:  /admin/* (branding, OIDC, app visibility, status)    │
            │                                                                   │
            │  Services: SessionStore · AuthManager · AppCatalog · Settings     │
            │  SQLite (/data/hearthport.db) · key file (/data/secret.key)       │
            └───────────────┬──────────────────────────────┬────────────────────┘
                            │ OIDC (discovery, token,      │ REST API
                            │ JWKS, end-session)           │ /api/v3/core/applications/
                            ▼                              ▼
                     ┌──────────────────── Authentik ───────────────────┐
                     └──────────────────────────────────────────────────┘
```

### Components
- **SessionStore**: server-side sessions in SQLite. The cookie holds only an opaque random ID (`HttpOnly`, `Secure`, `SameSite=Lax`). Sessions have idle and absolute timeouts.
- **AuthManager**: works out which login methods are enabled (§4), runs the local and OIDC login flows, and maps OIDC claims to roles.
- **AppCatalog**: fetches the user's permitted applications from Authentik, caches them per user, and applies local presentation overrides (§5).
- **Settings**: typed access to the settings stored in the DB, with encryption for secret fields.

## 4. Authentication model

### 4.1 Bootstrap admin (first run)

On startup:
1. If no local `admin` row exists, generate a 24-character random password (crypto/rand, base62), store its argon2id hash, and print a banner to **stdout**:
   ```
   ================================================================
    Hearthport initial admin credentials
      username: admin
      password: 7fQk2...  (shown once; change it after first login)
   ================================================================
   ```
2. The plaintext password is never written to disk. It is printed only when it is generated, so it is not repeated on every restart.
3. Recovery:
   - `HEARTHPORT_RESET_ADMIN_PASSWORD=true`: on next boot, generate and print a new password (logs a warning; unset the variable afterwards), or
   - `docker exec hearthport hearthport reset-admin-password` CLI subcommand.
4. Optional: `HEARTHPORT_ADMIN_PASSWORD_FILE` (Docker secret) sets the initial password instead of generating one.
5. After the first login the admin is prompted (optionally forced) to change the password.

### 4.2 OIDC configuration lifecycle

The OIDC config has three states: **none → draft → confirmed**.

| State | Meaning | Login options shown |
|---|---|---|
| `none` | Nothing saved | Local login only |
| `draft` | Saved but not yet proven to work | Local login only (+ "Test OIDC" button for admins) |
| `confirmed` | An admin completed a successful test login | **OIDC only** (unless the §4.3 override is set) |

Admin → Authentication page fields:
- Issuer URL (e.g. `https://auth.example.com/application/o/hearthport/`), fetched via `.well-known/openid-configuration`
- Client ID, client secret (encrypted at rest)
- Scopes (default `openid profile email groups`; see §5)
- Claim mappings: username (`preferred_username`), display name (`name`), email, groups (`groups`)
- **Admin group(s)**: Authentik group names whose members become Hearthport admins (e.g. `hearthport-admins`)
- Authentik API settings for app discovery (§5): base URL, API token (encrypted)
- Read-only: the redirect URI to paste into Authentik (`{BASE_URL}/auth/oidc/callback`)

**Test & confirm flow** (stops admins locking themselves out):
1. The admin clicks *Test & confirm*. Hearthport runs discovery and shows any errors inline.
2. The browser is sent through a real OIDC login using the draft settings.
3. On callback Hearthport verifies the ID token, extracts the claims, and checks that the authenticated user **matches an admin group**. It also checks that the Authentik API credentials can list applications.
4. The results page shows the received claims, the role that would be assigned, and the number of apps discovered.
5. Only if every check passes can the admin click **Confirm**. The state becomes `confirmed` and local login is switched off from then on (existing sessions stay valid until they expire).
6. Editing a confirmed config saves it as a new draft. The confirmed config stays active until the new draft is itself tested and confirmed.

### 4.3 Local-login override

| Env var | Effect |
|---|---|
| `HEARTHPORT_ENABLE_LOCAL_LOGIN=true` | The local `admin` username/password form is available **in addition to** OIDC even when OIDC is confirmed. This is the break-glass path, e.g. if Authentik is down or misconfigured. |

Rules:
- The decision is made on the **server** (`POST /auth/local` returns 404 when local login is disabled). Hiding the form in the UI is not enough.
- When the override is active, a warning banner is shown to admins and logged at startup.
- Local login is rate limited per IP and username, with exponential backoff and lockout.

Login-method truth table:

| OIDC confirmed? | `HEARTHPORT_ENABLE_LOCAL_LOGIN` | Login page offers |
|---|---|---|
| No | any | Username/password |
| Yes | unset/false | "Sign in with Authentik" only |
| Yes | true | Both |

### 4.4 OIDC login flow
- Authorization Code + **PKCE (S256)**, random `state` and `nonce` stored in a short-lived signed pre-auth cookie.
- ID token is verified (issuer, audience, expiry, nonce, signature via JWKS).
- Roles: `admin` if any configured admin group is in the `groups` claim, otherwise `user`. Roles are re-evaluated on every login and never stored permanently.
- Session stores: subject, username, display name, email, groups, role, and the access/refresh tokens (encrypted) if the delegated discovery mode is used.
- **Logout**: destroys the local session, then redirects to Authentik's `end_session_endpoint` with `id_token_hint` and `post_logout_redirect_uri`.
- Optional (v1.1): back-channel logout endpoint.

## 5. App discovery: "only show apps the user can access"

Authentik already knows which applications a user may access: it evaluates each
application's policy/group/user bindings. Its own *My applications* page uses
`GET /api/v3/core/applications/`, which returns only the apps the requesting
user can access. Hearthport reuses that decision instead of copying policies.

Two supported modes, chosen in the admin UI:

**Mode A: Service-account token (default, recommended)**
- Admin creates an Authentik service account + API token with permission to view applications and users.
- Hearthport calls `GET /api/v3/core/applications/?for_user=<pk>&page_size=100` (following pagination), where `<pk>` is the Authentik user ID.
- The user's `pk` is found by `GET /api/v3/core/users/?username=<preferred_username>` (cached), or read directly from a custom `ak_user_pk` claim added via an Authentik scope mapping.
- Pros: user tokens never gain API access; works the same for all users.

**Mode B: Delegated user token**
- Add Authentik's `goauthentik.io/api` scope to the Hearthport provider and request it at login. The user's access token is then used to call `GET /api/v3/core/applications/` directly.
- Pros: no service account; results are exactly what the user sees in Authentik.
- Cons: the portal holds a token with API access as that user (significant for Authentik admins), so tokens must be refreshed and stored encrypted.

> **Spike (Phase 0):** confirm, against the target Authentik version, that `for_user` gives the same results as the user's own library view (including `superuser_full_list` behaviour for Authentik superusers), and the minimum permissions the service account needs.

Handling the results:
- Fields used: `name`, `slug`, `group`, `meta_launch_url` (falls back to the provider's launch URL), `meta_icon`, `meta_description`, `meta_publisher`, `open_in_new_tab`.
- Apps with no launch URL are skipped. The Hearthport app itself is hidden.
- Icons: relative `meta_icon` paths are resolved against the Authentik base URL. Optionally Hearthport proxies and caches icons so the browser never calls Authentik's API directly.
- **Cache**: per-user, in memory, TTL 60s (configurable), cleared on login. A "refresh" button bypasses it.
- **Failure mode**: if Authentik is unreachable, show the last cached list with a stale badge. If there is no cache, show an error. Never fall back to showing all apps.

**Local presentation overrides** (admin UI, v1.1): per-app slug overrides for display order, category, icon, hidden flag, and pinned/favourite. These can only **hide or restyle** apps. They can never grant access to an app Authentik did not return.

## 6. UI / pages

| Route | Access | Purpose |
|---|---|---|
| `GET /login` | Public | Branding + info message; OIDC button and/or local form per §4.3 |
| `POST /auth/local` | Public (only if enabled) | Local admin login |
| `GET /auth/oidc/start` | Public | Begin OIDC flow |
| `GET /auth/oidc/callback` | Public | Complete OIDC flow (normal login or admin test) |
| `POST /logout` | Session | Logout (+ RP-initiated logout at Authentik) |
| `GET /` | User | Dashboard: app tiles grouped by Authentik `group`, search/filter |
| `GET /admin` | Admin | Status: OIDC state, override flag, Authentik reachability, version |
| `GET/POST /admin/branding` | Admin | Title, logo upload, Markdown login message, footer links, theme colour |
| `GET/POST /admin/auth` | Admin | OIDC + Authentik API config, Test & confirm |
| `GET/POST /admin/apps` | Admin | Presentation overrides (v1.1) |
| `GET/POST /admin/account` | Local admin | Change local admin password |
| `GET /healthz` / `GET /readyz` | Public | Liveness / readiness (DB reachable) |

The dashboard is responsive, supports light/dark mode, and works with the keyboard (`/` to focus search).

## 7. Data model (SQLite)

```sql
local_users   (id, username UNIQUE, password_hash, must_change BOOL, created_at, updated_at)
settings      (key PRIMARY KEY, value_json, updated_at)            -- branding, general prefs
oidc_configs  (id, state CHECK(state IN ('draft','confirmed','superseded')),
               issuer_url, client_id, client_secret_enc, scopes, claim_map_json,
               admin_groups_json, discovery_mode, ak_base_url, ak_token_enc,
               tested_at, tested_by, confirmed_at, confirmed_by, created_at)
sessions      (id PRIMARY KEY, user_kind, subject, data_enc, created_at, last_seen_at, expires_at)
app_overrides (slug PRIMARY KEY, hidden, sort_order, category, icon_url, updated_at)   -- v1.1
audit_log     (id, at, actor, action, detail_json)                  -- logins, config changes
```

"OIDC confirmed" means: a row exists with `state='confirmed'`.

## 8. Configuration (environment variables)

| Variable | Default | Purpose |
|---|---|---|
| `HEARTHPORT_BASE_URL` | *(required for OIDC)* | Public URL, used to build the redirect URI and secure cookies |
| `HEARTHPORT_LISTEN_ADDR` | `:8080` | Listen address |
| `HEARTHPORT_DATA_DIR` | `/data` | SQLite DB, secret key, uploaded logo |
| `HEARTHPORT_ENABLE_LOCAL_LOGIN` | `false` | Break-glass: allow local login after OIDC is confirmed |
| `HEARTHPORT_RESET_ADMIN_PASSWORD` | `false` | Regenerate and print the admin password on boot |
| `HEARTHPORT_ADMIN_PASSWORD_FILE` | — | Seed the initial admin password from a file/secret |
| `HEARTHPORT_SECRET_KEY` / `_FILE` | auto-generated to `/data/secret.key` | Key for at-rest encryption and cookie signing |
| `HEARTHPORT_TRUSTED_PROXIES` | — | CIDRs whose `X-Forwarded-*` headers are trusted |
| `HEARTHPORT_SESSION_TTL` | `12h` | Absolute session lifetime |
| `HEARTHPORT_APP_CACHE_TTL` | `60s` | Per-user app list cache |
| `HEARTHPORT_LOG_LEVEL` / `_FORMAT` | `info` / `text` | Logging (`json` available) |
| `HEARTHPORT_CA_FILE` | — | Extra CA bundle for a privately signed Authentik certificate |

Environment variables control deployment and the break-glass behaviour.
Everything about OIDC and branding lives in the web UI, as required.

## 9. Security checklist
- CSRF tokens on every state-changing form (double-submit or synchronizer token); `SameSite=Lax` cookies.
- Security headers: CSP (no inline scripts), `X-Frame-Options: DENY`, `Referrer-Policy`, HSTS when `BASE_URL` is https.
- Session ID is rotated on login and privilege change. Logout invalidates the session on the server.
- Rate limiting and audit logging for local login attempts. Password comparison uses constant time.
- Secrets are never logged. The client secret and API token are write-only in the UI (shown as "••• set").
- Outbound Authentik calls have timeouts, TLS verification is on (custom CA supported), and response sizes are capped.
- Open-redirect protection: post-login `return_to` must be a relative path.
- Uploaded logos are type-checked, size-capped, and re-served with a fixed content type.
- Container runs as a non-root user with a read-only root filesystem; only `/data` is writable.

## 10. Docker packaging

```dockerfile
FROM golang:1.25 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/hearthport ./cmd/hearthport

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/hearthport /hearthport
VOLUME /data
EXPOSE 8080
USER nonroot
HEALTHCHECK CMD ["/hearthport", "healthcheck"]
ENTRYPOINT ["/hearthport"]
```

Example `docker-compose.yml`:
```yaml
services:
  hearthport:
    image: ghcr.io/kahooli/hearthport:latest
    restart: unless-stopped
    ports: ["8080:8080"]
    environment:
      HEARTHPORT_BASE_URL: https://portal.example.com
      # HEARTHPORT_ENABLE_LOCAL_LOGIN: "true"   # break-glass only
    volumes:
      - ./data:/data
```

Multi-arch images (`linux/amd64`, `linux/arm64`) are published to GHCR by a GitHub Actions workflow on tags.

## 11. Proposed repository layout

```
cmd/hearthport/           main.go (serve, reset-admin-password, healthcheck subcommands)
internal/config/          env parsing
internal/store/           SQLite, migrations (embedded), repositories
internal/crypto/          key management, AES-GCM, argon2id
internal/auth/            local login, OIDC client, login-method policy, sessions, CSRF
internal/authentik/       API client, app discovery modes A/B, cache
internal/web/             router, handlers, middleware, templates/, static/
docs/                     PLAN.md, authentik-setup.md
deploy/                   docker-compose.yml examples
Dockerfile, Makefile, .github/workflows/
```

## 12. Roadmap

**Phase 0: Spike (short)**
- Stand up Authentik in docker-compose for development. Create an OAuth2/OpenID provider + application and a few test apps with group bindings.
- Verify app discovery via `for_user` (Mode A) and `goauthentik.io/api` (Mode B), and write down the permissions and claims actually needed.

**Phase 1: Skeleton & bootstrap**
- Go module, config, SQLite + migrations, structured logging, `/healthz`.
- Bootstrap admin with password banner, local login, sessions, CSRF, logout.
- Base layout/templates, login page with static branding.
- Dockerfile and compose file; CI (lint with `golangci-lint`, `go test`, image build).

**Phase 2: Admin & OIDC**
- Admin area; branding editor (title, logo, Markdown message sanitized with `bluemonday`).
- OIDC config draft/test/confirm lifecycle; encrypted secrets.
- Login-method policy + `HEARTHPORT_ENABLE_LOCAL_LOGIN` override; RP-initiated logout.
- Admin role mapping from groups.

**Phase 3: App discovery & dashboard**
- Authentik API client, Mode A + Mode B, pagination, cache, stale fallback.
- Dashboard tiles grouped by category, search, icons, open-in-new-tab.

**Phase 4: Hardening & release**
- Security headers, rate limiting, audit log view, reset-password CLI.
- Tests: unit tests (policy truth table, claim mapping, crypto), integration tests with a mock OIDC provider, and an end-to-end test (Playwright) against a real Authentik container in CI.
- `docs/authentik-setup.md` (step-by-step provider, scope mapping, service account), multi-arch release to GHCR, v1.0.0.

**Later (v1.1+)**
- App presentation overrides, per-user favourites, back-channel logout, Prometheus `/metrics`, extra non-Authentik links shown to everyone or by group, i18n.

## 13. Acceptance criteria (v1)
1. A fresh container prints admin credentials once. Logging in with them works; restarting does not print them again.
2. Before OIDC is confirmed, only local login is offered.
3. An admin can save OIDC settings, test them, and cannot confirm unless the test user is in an admin group.
4. After confirmation, `/login` shows only the Authentik button and `POST /auth/local` returns 404.
5. With `HEARTHPORT_ENABLE_LOCAL_LOGIN=true`, both options appear and local login works.
6. Two Authentik users with different group bindings each see only their permitted apps. Removing a binding in Authentik removes the tile within the cache TTL.
7. If Authentik is unreachable, the dashboard never shows apps the user wasn't previously permitted to see.

## 14. Open questions
1. Stack preference: Go + htmx as proposed, or would you prefer something else (e.g. Python/FastAPI or TypeScript/SvelteKit)?
2. Default discovery mode: service-account token (A) or delegated user token (B)?
3. Should non-admin OIDC users be allowed in at all by default, or only members of a configured "users" group? (Authentik can also enforce this with a policy on the Hearthport application itself, which is the recommended approach.)
4. Force the bootstrap admin to change the password on first login, or keep it optional?
5. Is there a need for extra links that don't come from Authentik (e.g. external bookmarks)?
