# Phase 0 spike: findings

**Date:** 2026-10-01
**Goal:** before writing Hearthport, check the APIs it depends on behave the way [the plan](../PLAN.md) assumes.

## How it was tested

- **Throwaway stack** ([`deploy/dev/compose.spike.yml`](../../deploy/dev/compose.spike.yml)), matched to the versions running on the target server where possible:

  | Service | Version tested | Target server |
  |---|---|---|
  | Authentik | 2026.8.3 | 2026.8 |
  | Seerr | 3.5.0 (upstream) | `ghcr.io/kahooli/seerrng` (a Seerr fork) |
  | Jellyfin | 12.1.0 | not installed |
  | Plex Media Server | 1.43.4 (unclaimed) | `linuxserver/plex:latest` |
  | Tautulli | not run | `linuxserver/tautulli` |

- **Test data** comes from [`deploy/dev/seed.py`](../../deploy/dev/seed.py):
  - users alice, bob, carol and dave in different groups
  - apps bound to groups, plus one app with no bindings
  - a least-privilege service account
  - Jellyfin users with different library access
  - Seerr users with their own requests
- **Probe tool:** [`cmd/hearthport-probe`](../../cmd/hearthport-probe) is a read-only command (it only sends GET requests) that runs the same checks against any server. It has unit tests with fake servers in `internal/probe`.
- **Not testable from the build sandbox:** plex.tv, TMDB and `ghcr.io` downloads are blocked or refused there. Those parts are marked **to confirm** below, and the probe checks them on the real network ([Running the probe](#running-the-probe-against-your-servers)).

## Summary

| # | Question | Result |
|---|---|---|
| 1 | Can a non-superuser token list a given user's apps (`for_user`)? | **Yes**, with the `authentik_core.view_user_applications` permission. |
| 2 | Does `for_user` match what the user sees in Authentik? | **Yes.** Group bindings were respected for every test user. |
| 3 | Do apps with no bindings show to everyone? | **Yes.** Confirms the §4.5 risk; Hearthport should warn about them. |
| 4 | Can Hearthport check access to its own app with `check_access`? | **No, don't use it.** For a non-superuser token it silently checks the *token's* own account instead (here: "not allowed" for everyone). |
| 5 | Is paging the app list straightforward? | **No.** Authentik pages *before* filtering per user, so pages can be short or empty while more follow. |
| 6 | Can Seerr return only one user's requests? | **Yes**, and Seerr enforces it itself when called with `X-API-User`. |
| 7 | Can Hearthport users be matched to Seerr users by email? | **Yes, exact match only.** `?q=` is a substring search. Jellyfin-imported users have no real email. |
| 8 | Does Jellyfin "recently added" respect library access with one API key? | **Yes.** |
| 9 | Do Plex's library and recently-added APIs work as planned? | **Yes** on 1.43.4. Per-user sharing via plex.tv is **to confirm**. |

## Authentik (2026.8.3)

### Permissions for the service account
- **App listing:** `GET /api/v3/core/applications/?for_user=<pk>` needs **`authentik_core.view_user_applications`**. Without it the API answers `400 {"for_user":"User not found"}`, which looks like a bad user ID rather than a missing permission. Hearthport's setup check should explain this.
- **Looking up a user's pk by username** (`/core/users/?username=`) needs **`authentik_core.view_user`**. It isn't needed if the pk comes from a claim (see below).
- **Token type:** the token returned by *Create service account* is an app password and is **rejected by the API** ("Token invalid/expired"). An **API-intent token** must be created for the service account under *Directory → Tokens and App passwords*. The setup guide must say this.
- **Assigning the permissions:** create a role (e.g. `hearthport-discovery`) with these permissions and assign it to the service account. Roles can be assigned to a user directly in 2026.8.

### Behaviour
- **Results:** `for_user` results matched the group bindings exactly:

  | User | Groups | Apps returned |
  |---|---|---|
  | alice | media + books | 6 |
  | bob | media | 5 |
  | carol | none | only the unbound app |
  | dave | admins + media | the admin tool and the media apps |

- **Apps with no bindings are returned for every user.** This is the `core_default_app_access` system flag, on by default. Planned change: the admin status page warns about unbound apps when the token can read bindings (`authentik_policies.view_policybinding`, optional). The probe already does this.
- **Superusers:** the plain app list (no `for_user`) is filtered by the *caller's own* access, **even for a superuser**, unless `superuser_full_list=true` is passed.
- **`check_access` is unsafe for Hearthport:** `/core/applications/<slug>/check_access/?for_user=` only honours `for_user` when the token belongs to a **superuser**. Otherwise it evaluates the token's own user and returns `passing: false` without an error. Hearthport should check its own access by looking for its slug in the `for_user` list, as planned in §4.5.
- **Paging:** the list is **paged before it is filtered**. With `page_size=2`, carol's first page was empty while `next` was 2, and `count` is the total of *all* apps (7), not the user's. Hearthport must follow `next` until it is 0 and ignore `count`. Use `page_size=100` to keep the number of calls down.
- **New application fields** Hearthport should use:
  - `meta_hide`: the admin chose to hide the app from the user library; Hearthport should respect it.
  - `meta_icon_url` and `meta_icon_themed_urls`: ready-made icon URLs, including light and dark variants.
  - `launch_url`: already resolved, falls back to the provider's URL, and is empty when there isn't one.
- **Default `sub`:** the default `sub_mode` is `hashed_user_id`, which equals the user's `uid`. The users API can't be filtered by `uid`, so mapping `sub` to the user's pk needs either a username lookup (`view_user`) or **a custom scope mapping that adds the pk as a claim** (e.g. `return {"ak_pk": request.user.pk}`). The claim is simpler and needs fewer permissions; the setup guide will offer it, with username lookup as the fallback.
- **Docker note:** the test container had no IPv6, and Authentik's default `[::]` listeners crashed it on start. Setting `AUTHENTIK_LISTEN__HTTP/HTTPS/METRICS` to `0.0.0.0:…` fixed it. This doesn't affect Hearthport but is worth knowing for the compose examples.

## Seerr (3.5.0)

- **API key with `X-API-User`:** an API key plus `X-API-User: <seerr user id>` acts *as that user* (`middleware/auth.js`). For a normal user Seerr then:
  - returns only that user's requests from `GET /api/v1/request`, and
  - answers `403 "You do not have permission to view this user's requests."` when asked for someone else's with `requestedBy`.

  Hearthport should **always send `X-API-User`** as well as `requestedBy`, so Seerr enforces the scope even if Hearthport has a bug.
- **Request results are thin:** they carry only `media.tmdbId` and status. **Titles and posters need `/movie/{id}` or `/tv/{id}`**, which Seerr proxies to TMDB. Hearthport should cache these lookups (they rarely change) and show the request without a title if the lookup fails. These lookups couldn't run here because TMDB refused the sandbox; the probe checks them.
- **User search:** `GET /api/v1/user?q=` is a case-insensitive **substring** match over username, email, Plex username and Jellyfin username. Hearthport must keep only an **exact, case-insensitive email match**, and treat zero or several matches as "not linked".
- **Jellyfin-imported users:** they get their **username as their email** until they set one. Plex-imported users get their plex.tv email, which suits the target server (Plex). Planned change: add an optional username match as a fallback (already allowed by §5.3, "email or username").
- **To confirm:** the target server runs a fork (`seerrng`). The probe confirms the same endpoints and headers work there.

## Jellyfin (12.1.0)

- **Per-user results with one API key:** `GET /Items/Latest?userId=<id>` (and `/Users/<id>/Items/Latest`) returns only items from libraries that user can access. Alice (Movies only) got 2 items; Bob (Movies + TV) got 3.
- **User matching:** `GET /Users` with the API key lists users, for matching by username.
- **Not on the target server:** Jellyfin isn't installed there, so its integration is lower priority (see "Plan changes").

## Plex (1.43.4)

- **Confirmed on a local unclaimed server:**
  - `GET /identity` returns `machineIdentifier` and `version`.
  - `GET /library/sections` (JSON with `Accept: application/json`) returns sections with `key`, `title` and `type`.
  - `GET /library/sections/<key>/recentlyAdded?X-Plex-Container-Size=N` returns items with `title`, `type`, `thumb` and `addedAt`.
- **Per-user sharing (to confirm):** the plan assumed Plex can't say which libraries are shared with each user. Two sources should be able to:
  1. **plex.tv** `GET /api/servers/<machineIdentifier>/shared_servers` with the owner's token. It returns XML with one `SharedServer` per user and `Section shared="1"` entries. This was blocked from the sandbox; the probe checks it. The response also contains each user's access token, which Hearthport must never store or log.
  2. **Tautulli** `cmd=get_users` returns `shared_libraries` for each user. Tautulli runs on the target server; the probe checks it.

  If either works, Hearthport can show each user only the Plex libraries actually shared with them, instead of relying on the admin to set group visibility by hand (§5.3).

## Plan changes this leads to

Applied to [`docs/PLAN.md`](../PLAN.md) in the same change:

- **§5 Authentik:**
  - the exact permissions and token type
  - follow `next` across empty pages and ignore `count`
  - never use `check_access`
  - respect `meta_hide`; use `meta_icon_url`, `meta_icon_themed_urls` and `launch_url`
  - optional `ak_pk` claim to avoid needing `view_user`
- **§4.5:** warn admins about apps with no bindings.
- **§5.3 Seerr:**
  - always send `X-API-User`
  - exact email match, with username as a fallback
  - cache title and poster lookups
- **§5.3 Plex:** use per-user sharing from plex.tv, or Tautulli, when available; fall back to group visibility.
- **§12 Phase 0:** marked done, with the remaining real-network checks listed.

**Decided by the project owner (2026-10-02):**
- **Jellyfin moves to "later"**, because the target server runs Plex. Its design note stays in §5.3.
- **SQLite stays the default**, and PostgreSQL is used when `HEARTHPORT_DATABASE_URL` is set. The owner's deployment will use their existing PostgreSQL 18 server, so CI tests the store against PostgreSQL 14 and 18.

## Running the probe against your servers

The probe only reads (HTTP GET), masks email addresses in its report, and never prints tokens. Run it on a machine that can reach your servers; it needs Go 1.24 or newer.

```sh
git clone https://github.com/KaHooli/Hearthport && cd Hearthport
export HP_AUTHENTIK_URL=https://auth.example.com
export HP_AUTHENTIK_TOKEN=…            # API-intent token of the Hearthport service account
export HP_AUTHENTIK_APP_SLUG=hearthport  # if you've created the app already
export HP_AUTHENTIK_TEST_USERS=you,a-family-member
export HP_SEERR_URL=https://seerr.example.com HP_SEERR_API_KEY=… HP_SEERR_TEST_EMAILS=you@example.com
export HP_PLEX_URL=http://plex.lan:32400 HP_PLEX_TOKEN=…   # owner's X-Plex-Token
export HP_TAUTULLI_URL=http://tautulli.lan:8181 HP_TAUTULLI_API_KEY=…
go run ./cmd/hearthport-probe > probe-report.md
```

Leave any service's URL unset to skip it. Check the report before sharing it. Titles in "recently added" are shown as-is, and usernames appear in the Plex sharing line.

## Reproducing the throwaway stack

```sh
docker compose -f deploy/dev/compose.spike.yml up -d
python3 deploy/dev/seed.py > /tmp/hp-env.sh && . /tmp/hp-env.sh
go run ./cmd/hearthport-probe
docker compose -f deploy/dev/compose.spike.yml down -v   # when finished
```

The stack uses fixed development passwords and must not be exposed to a network. It keeps a Jellyfin container even though Jellyfin integration is postponed, because the test Seerr needs a media server to sign its users in.
