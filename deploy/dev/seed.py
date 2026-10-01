#!/usr/bin/env python3
"""Seed the throwaway spike stack (deploy/dev/compose.spike.yml) with test data.

Creates, idempotently:
  Authentik  groups media-users, books-users, hearthport-admins; users alice, bob,
             carol, dave; an OAuth2 provider + "hearthport" app; test apps with
             group bindings (and one with none); a service account with an API
             token and the minimal "hearthport-discovery" role.
  Jellyfin   admin jfadmin, Movies + TV libraries with tiny generated files,
             users alice (Movies only) and bob (both), an API key.
  Seerr      initialised against Jellyfin, alice and bob imported with emails.

Prints the HP_* environment variables for cmd/hearthport-probe.
Dev only: uses fixed, well-known passwords. Python 3 standard library only.
"""
import json, subprocess, sys, time, urllib.error, urllib.parse, urllib.request
from http.cookiejar import CookieJar

AK, JF, SR = "http://127.0.0.1:9000/api/v3", "http://127.0.0.1:8096", "http://127.0.0.1:5055/api/v1"
AK_BOOTSTRAP = "dev-bootstrap-token-0123456789abcdef"
JF_AUTH = 'MediaBrowser Client="hearthport-seed", Device="seed", DeviceId="hearthport-seed", Version="0.1"'
COMPOSE = ["docker", "compose", "-f", "deploy/dev/compose.spike.yml"]


def http(method, url, body=None, headers=None, opener=None):
    h = {"Content-Type": "application/json", "Accept": "application/json"}
    h.update(headers or {})
    data = json.dumps(body).encode() if body is not None else None
    op = opener or urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with op.open(urllib.request.Request(url, method=method, data=data, headers=h), timeout=60) as r:
            raw = r.read()
            try:
                return r.status, (json.loads(raw) if raw else None)
            except ValueError:
                return r.status, raw.decode(errors="replace")  # e.g. Jellyfin's plain-text /health
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode(errors="replace")[:300]


def must(res, what):
    status, body = res
    if status >= 300:
        sys.exit(f"{what}: HTTP {status}: {body}")
    return body


def wait(url, ok=(200, 204), tries=120):
    for _ in range(tries):
        try:
            if http("GET", url)[0] in ok:
                return
        except OSError:
            pass
        time.sleep(2)
    sys.exit(f"timed out waiting for {url}")


# ---------------------------------------------------------------- Authentik
def ak(method, path, body=None, token=AK_BOOTSTRAP):
    return http(method, AK + path, body, {"Authorization": "Bearer " + token})


def ak_get_or_create(path, key, val, body):
    # The application list is filtered by the caller's own access, even for a
    # superuser, unless superuser_full_list is set.
    extra = "&superuser_full_list=true" if path == "/core/applications/" else ""
    found = must(ak("GET", f"{path}?{key}={urllib.parse.quote(str(val))}{extra}"), path)["results"]
    return found[0] if found else must(ak("POST", path, body), f"create {path} {val}")


def seed_authentik():
    wait("http://127.0.0.1:9000/-/health/ready/")
    # The worker creates the bootstrap token shortly after the server is ready.
    for _ in range(90):
        if ak("GET", "/core/users/me/")[0] == 200:
            break
        time.sleep(2)
    groups = {n: ak_get_or_create("/core/groups/", "name", n, {"name": n}) for n in ["media-users", "books-users", "hearthport-admins"]}
    members = {"alice": ["media-users", "books-users"], "bob": ["media-users"], "carol": [], "dave": ["hearthport-admins", "media-users"]}
    for u, gs in members.items():
        ak_get_or_create("/core/users/", "username", u, {"username": u, "name": u.title(), "email": f"{u}@example.test",
                                                         "groups": [groups[g]["pk"] for g in gs], "is_active": True})

    # Default flows and scope mappings come from blueprints the worker applies
    # in the background on first start.
    def flow(designation, word):
        for _ in range(90):
            flows = must(ak("GET", f"/flows/instances/?designation={designation}"), "flows")["results"]
            match = [f["pk"] for f in flows if word in f["slug"]]
            if match:
                return match[0]
            time.sleep(2)
        sys.exit(f"no default {designation} flow appeared")
    authz, inval = flow("authorization", "implicit"), flow("invalidation", "provider")
    key = must(ak("GET", "/crypto/certificatekeypairs/?has_key=true"), "keys")["results"][0]["pk"]
    maps = [m["pk"] for m in must(ak("GET", "/propertymappings/provider/scope/?managed__startswith=goauthentik.io/providers/oauth2/scope-"), "maps")["results"]]
    provider = ak_get_or_create("/providers/oauth2/", "name", "hearthport", {
        "name": "hearthport", "authorization_flow": authz, "invalidation_flow": inval, "client_type": "confidential",
        "client_id": "hearthport-dev", "client_secret": "hearthport-dev-secret", "signing_key": key, "property_mappings": maps,
        "redirect_uris": [{"matching_mode": "strict", "url": "http://localhost:8080/auth/oidc/callback"}]})

    apps = {  # slug: (name, provider, launch url, bound group)
        "hearthport": ("Hearthport", provider["pk"], "", "media-users"),
        "plex": ("Plex", None, "http://plex.example.test", "media-users"),
        "seerr": ("Seerr", None, "http://seerr.example.test", "media-users"),
        "audiobookshelf": ("Audiobookshelf", None, "http://abs.example.test", "books-users"),
        "admin-tool": ("Admin tool", None, "http://admin.example.test", "hearthport-admins"),
        "no-launch": ("No launch URL", None, "", "media-users"),
        "unbound-app": ("Unbound app (no bindings)", None, "http://open.example.test", None),
    }
    for slug, (name, prov, url, grp) in apps.items():
        app = ak_get_or_create("/core/applications/", "slug", slug, {"name": name, "slug": slug, "provider": prov, "meta_launch_url": url})
        if grp and not must(ak("GET", f"/policies/bindings/?target={app['pbm_uuid']}"), "bindings")["results"]:
            must(ak("POST", "/policies/bindings/", {"target": app["pbm_uuid"], "group": groups[grp]["pk"], "order": 0, "enabled": True}), "bind")

    sa = must(ak("GET", "/core/users/?username=hearthport-sa"), "sa")["results"]
    sa_pk = sa[0]["pk"] if sa else must(ak("POST", "/core/users/service_account/", {"name": "hearthport-sa", "create_group": False, "expiring": False}), "sa")["user_pk"]
    # The token returned by service_account/ is an app password; the API needs an "api" intent token.
    if ak("GET", "/core/tokens/hearthport-api/")[0] == 404:
        must(ak("POST", "/core/tokens/", {"identifier": "hearthport-api", "intent": "api", "user": sa_pk, "expiring": False}), "token")
    token = must(ak("GET", "/core/tokens/hearthport-api/view_key/"), "view key")["key"]
    role = ak_get_or_create("/rbac/roles/", "name", "hearthport-discovery", {"name": "hearthport-discovery"})
    must(ak("POST", f"/rbac/permissions/assigned_by_roles/{role['pk']}/assign/",
            {"permissions": ["authentik_core.view_user_applications", "authentik_core.view_user"]}), "assign perms")
    must(ak("POST", f"/rbac/roles/{role['pk']}/add_user/", {"pk": sa_pk}), "add role")
    return {"HP_AUTHENTIK_URL": "http://127.0.0.1:9000", "HP_AUTHENTIK_TOKEN": token, "HP_AUTHENTIK_TEST_USERS": "alice,bob,carol,dave"}


# ----------------------------------------------------------------- Jellyfin
def jf(method, path, body=None, token=None):
    auth = JF_AUTH + (f', Token="{token}"' if token else "")
    return http(method, JF + path, body, {"Authorization": auth})


def seed_jellyfin():
    wait(JF + "/health")
    if not must(jf("GET", "/System/Info/Public"), "info").get("StartupWizardCompleted"):
        must(jf("POST", "/Startup/Configuration", {"UICulture": "en-US", "MetadataCountryCode": "AU", "PreferredMetadataLanguage": "en"}), "startup")
        jf("GET", "/Startup/User")
        must(jf("POST", "/Startup/User", {"Name": "jfadmin", "Password": "jfadmin-dev"}), "startup user")
        must(jf("POST", "/Startup/Complete"), "startup complete")
    admin = must(jf("POST", "/Users/AuthenticateByName", {"Username": "jfadmin", "Pw": "jfadmin-dev"}), "login")["AccessToken"]

    gen = ('FF=/usr/lib/jellyfin-ffmpeg/ffmpeg; mk(){ mkdir -p "$(dirname "$1")"; [ -f "$1" ] || $FF -loglevel error '
           '-f lavfi -i testsrc=duration=2:size=320x240:rate=10 -f lavfi -i sine=duration=2 -shortest -y "$1"; }; '
           'mk "/media/movies/Alpha Movie (2021)/Alpha Movie (2021).mkv"; mk "/media/movies/Beta Movie (2022)/Beta Movie (2022).mkv"; '
           'mk "/media/tv/Gamma Show (2020)/Season 01/Gamma Show S01E01.mkv"')
    subprocess.run(COMPOSE + ["exec", "-T", "jellyfin", "sh", "-c", gen], check=True)

    libs = {l["Name"]: l["Id"] for l in must(jf("GET", "/Library/MediaFolders", token=admin), "libs")["Items"]}
    for name, kind, path in [("Movies", "movies", "/media/movies"), ("TV Shows", "tvshows", "/media/tv")]:
        if name not in libs:
            q = urllib.parse.urlencode({"name": name, "collectionType": kind, "refreshLibrary": "true"})
            must(jf("POST", f"/Library/VirtualFolders?{q}", {"LibraryOptions": {"PathInfos": [{"Path": path}]}}, admin), "library")
    jf("POST", "/Library/Refresh", token=admin)
    time.sleep(15)
    libs = {l["Name"]: l["Id"] for l in must(jf("GET", "/Library/MediaFolders", token=admin), "libs")["Items"]}

    users = {u["Name"]: u for u in must(jf("GET", "/Users", token=admin), "users")}
    for name, allowed in {"alice": ["Movies"], "bob": ["Movies", "TV Shows"]}.items():
        uid = users[name]["Id"] if name in users else must(jf("POST", "/Users/New", {"Name": name, "Password": "pw"}, admin), "user")["Id"]
        policy = must(jf("GET", f"/Users/{uid}", token=admin), "user")["Policy"]
        policy.update(EnableAllFolders=False, EnabledFolders=[libs[x] for x in allowed])
        must(jf("POST", f"/Users/{uid}/Policy", policy, admin), "policy")

    keys = must(jf("GET", "/Auth/Keys", token=admin), "keys")["Items"]
    if not any(k["AppName"] == "hearthport" for k in keys):
        must(jf("POST", "/Auth/Keys?app=hearthport", token=admin), "api key")
        keys = must(jf("GET", "/Auth/Keys", token=admin), "keys")["Items"]
    key = next(k["AccessToken"] for k in keys if k["AppName"] == "hearthport")
    return {"HP_JELLYFIN_URL": JF, "HP_JELLYFIN_API_KEY": key, "HP_JELLYFIN_TEST_USERS": "alice,bob"}


# -------------------------------------------------------------------- Seerr
def seed_seerr():
    wait(SR + "/status")
    op = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(CookieJar()))
    sr = lambda m, p, b=None, h=None: http(m, SR + p, b, h, op)
    if not must(sr("GET", "/settings/public"), "public")["initialized"]:
        must(sr("POST", "/auth/jellyfin", {"username": "jfadmin", "password": "jfadmin-dev", "hostname": "jellyfin", "port": 8096,
                                            "useSsl": False, "urlBase": "", "email": "jfadmin@example.test", "serverType": 2}), "seerr login")
        jf_users = must(sr("GET", "/settings/jellyfin/users"), "jf users")
        must(sr("POST", "/user/import-from-jellyfin", {"jellyfinUserIds": [u["id"] for u in jf_users if u["username"] in ("alice", "bob")]}), "import")
        must(sr("POST", "/settings/initialize"), "initialize")
    else:
        must(sr("POST", "/auth/jellyfin", {"username": "jfadmin", "password": "jfadmin-dev"}), "seerr login")
    key = must(sr("GET", "/settings/main"), "settings")["apiKey"]
    hdr = {"X-Api-Key": key}
    for u in must(sr("GET", "/user?take=50", h=hdr), "users")["results"]:
        name = u.get("jellyfinUsername")
        if name in ("alice", "bob") and u["email"] != f"{name}@example.test":
            # Jellyfin imports use the username as email; give them real-looking ones.
            must(sr("POST", f"/user/{u['id']}/settings/main", {"email": f"{name}@example.test", "username": name}, hdr), "email")
    return {"HP_SEERR_URL": "http://127.0.0.1:5055", "HP_SEERR_API_KEY": key, "HP_SEERR_TEST_EMAILS": "alice@example.test,bob@example.test"}


if __name__ == "__main__":
    env = {}
    for step in (seed_authentik, seed_jellyfin, seed_seerr):
        print(f"seeding {step.__name__[5:]}…", file=sys.stderr)
        env.update(step())
    print("export NO_PROXY=127.0.0.1,localhost")
    for k, v in env.items():
        print(f"export {k}={v}")
