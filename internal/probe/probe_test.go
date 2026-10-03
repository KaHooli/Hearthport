package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func find(r *Report, check string) []Result {
	var out []Result
	for _, res := range r.Results {
		if res.Check == check {
			out = append(out, res)
		}
	}
	return out
}

// Authentik paginates before filtering by policy, so a user can get an empty
// first page and still have apps on later pages. All pages must be followed.
func TestAuthentikFollowsEmptyPages(t *testing.T) {
	pages := map[string]string{
		"1": `{"pagination":{"next":2,"count":5},"results":[]}`,
		"2": `{"pagination":{"next":3,"count":5},"results":[{"slug":"plex","launch_url":"http://p"}]}`,
		"3": `{"pagination":{"next":0,"count":5},"results":[{"slug":"hearthport","launch_url":"http://h"}]}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			t.Errorf("probe sent %s; it must only send GET", req.Method)
		}
		switch req.URL.Path {
		case "/core/users/me/":
			fmt.Fprint(w, `{"user":{"pk":9,"username":"sa"}}`)
		case "/core/users/":
			fmt.Fprint(w, `{"pagination":{"next":0},"results":[{"pk":5,"username":"alice"}]}`)
		case "/core/applications/":
			if req.URL.Query().Get("superuser_full_list") == "true" {
				fmt.Fprint(w, `{"pagination":{"next":0},"results":[]}`)
				return
			}
			fmt.Fprint(w, pages[req.URL.Query().Get("page")])
		default:
			http.Error(w, "forbidden", http.StatusForbidden)
		}
	}))
	defer srv.Close()

	r := &Report{}
	c := NewClient(srv.URL, nil, defaultTimeout)
	ProbeAuthentik(context.Background(), AuthentikConfig{AppSlug: "hearthport", TestUsers: []string{"alice"}}, c, r)

	apps := find(r, "apps for user")
	if len(apps) != 1 || apps[0].Level != Pass || !strings.Contains(apps[0].Detail, "2 apps (3 pages, 1 empty pages") {
		t.Fatalf("unexpected result: %+v", apps)
	}
	if got := find(r, "Hearthport access"); len(got) != 1 || got[0].Level != Pass {
		t.Fatalf("hearthport slug on page 3 not found: %+v", got)
	}
}

func TestAuthentikMissingPermissionExplained(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/core/users/me/":
			fmt.Fprint(w, `{"user":{"pk":9,"username":"sa"}}`)
		case "/core/users/":
			fmt.Fprint(w, `{"results":[{"pk":5,"username":"alice"}]}`)
		case "/core/applications/":
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"for_user":"User not found"}`)
		default:
			http.Error(w, "forbidden", http.StatusForbidden)
		}
	}))
	defer srv.Close()
	r := &Report{}
	ProbeAuthentik(context.Background(), AuthentikConfig{TestUsers: []string{"alice"}}, NewClient(srv.URL, nil, defaultTimeout), r)
	got := find(r, "apps for user")
	if len(got) != 1 || got[0].Level != Fail || !strings.Contains(got[0].Detail, "view_user_applications") {
		t.Fatalf("expected a FAIL naming the missing permission, got %+v", got)
	}
}

// Seerr's ?q= is a substring search, so only an exact email match counts, and
// requests are fetched with X-API-User so Seerr enforces the scope itself.
func TestSeerrExactEmailAndScopedRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/v1/status":
			fmt.Fprint(w, `{"version":"3.5.0"}`)
		case "/api/v1/user":
			if req.URL.Query().Get("q") == "" {
				fmt.Fprint(w, `{"pageInfo":{"results":3},"results":[]}`)
				return
			}
			fmt.Fprint(w, `{"results":[{"id":2,"email":"alice@example.test","userType":1},{"id":4,"email":"malice@example.test.au","userType":1}]}`)
		case "/api/v1/request":
			if req.Header.Get("X-Api-User") != "2" {
				t.Errorf("requests fetched without X-API-User=2 (got %q)", req.Header.Get("X-Api-User"))
			}
			fmt.Fprint(w, `{"pageInfo":{"results":1},"results":[{"id":1,"type":"movie","requestedBy":{"id":2},"media":{"tmdbId":603}}]}`)
		case "/api/v1/movie/603":
			fmt.Fprint(w, `{"title":"The Matrix","posterPath":"/x.jpg"}`)
		default:
			http.NotFound(w, req)
		}
	}))
	defer srv.Close()
	r := &Report{Redact: true}
	ProbeSeerr(context.Background(), SeerrConfig{URL: srv.URL, APIKey: "k", TestEmails: []string{"Alice@Example.test"}}, r)

	m := find(r, "user matching")
	if len(m) != 1 || m[0].Level != Pass || !strings.Contains(m[0].Detail, "Seerr user 2") {
		t.Fatalf("expected exact match to user 2, got %+v", m)
	}
	if strings.Contains(m[0].Detail, "lice@") {
		t.Fatalf("email not redacted: %q", m[0].Detail)
	}
	if o := find(r, "own requests"); len(o) != 1 || o[0].Level != Pass {
		t.Fatalf("own requests: %+v", o)
	}
}

func TestPlexSharedServersParsedWithoutTokens(t *testing.T) {
	const sharedXML = `<MediaContainer><SharedServer id="1" userID="11" username="bob" email="bob@example.test" accessToken="SECRET-SHARED-TOKEN" allLibraries="0">
<Section id="1" key="1" title="Movies" type="movie" shared="1"/><Section id="2" key="2" title="TV" type="show" shared="0"/></SharedServer></MediaContainer>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("X-Plex-Token") != "owner-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case req.URL.Path == "/identity":
			fmt.Fprint(w, `{"MediaContainer":{"machineIdentifier":"abc","version":"1.42"}}`)
		case req.URL.Path == "/library/sections":
			fmt.Fprint(w, `{"MediaContainer":{"Directory":[{"key":"1","title":"Movies","type":"movie"}]}}`)
		case strings.HasSuffix(req.URL.Path, "/recentlyAdded"):
			fmt.Fprint(w, `{"MediaContainer":{"Metadata":[{"title":"Alpha","type":"movie","thumb":"/t"}]}}`)
		case req.URL.Path == "/api/servers/abc/shared_servers":
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, sharedXML)
		default:
			http.NotFound(w, req)
		}
	}))
	defer srv.Close()
	r := &Report{Redact: true}
	ProbePlex(context.Background(), PlexConfig{URL: srv.URL, Token: "owner-token", PlexTV: srv.URL}, r)

	s := find(r, "per-user sharing")
	if len(s) != 1 || s[0].Level != Pass || !strings.Contains(s[0].Detail, "bob: 1/2") {
		t.Fatalf("unexpected sharing result: %+v", s)
	}
	out, _ := json.Marshal(r.Results)
	if strings.Contains(string(out), "SECRET") || strings.Contains(string(out), "owner-token") {
		t.Fatal("report contains a token")
	}
}

func TestClientRefusesCrossHostRedirect(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("followed a redirect to another host")
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, strings.Replace(other.URL, "127.0.0.1", "localhost", 1)+"/x", http.StatusFound)
	}))
	defer srv.Close()
	if _, err := NewClient(srv.URL, nil, defaultTimeout).Get(context.Background(), "/", nil); err == nil {
		t.Fatal("expected an error for a cross-host redirect")
	}
}

func TestRedactEmails(t *testing.T) {
	got := RedactEmails("alice@example.test and bob.smith@mail.example.com")
	want := "a***@example.test and b***@mail.example.com"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// With "name=pk" the probe skips the user lookup, as Hearthport does when the
// ID token carries an ak_pk claim, so a token without view_user still works.
func TestAuthentikUserPKSkipsLookup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/core/users/me/":
			fmt.Fprint(w, `{"user":{"pk":9,"username":"sa"}}`)
		case "/core/users/":
			t.Error("looked up a user although its pk was given")
			http.Error(w, "forbidden", http.StatusForbidden)
		case "/core/applications/":
			if req.URL.Query().Get("for_user") != "5" {
				fmt.Fprint(w, `{"pagination":{"next":0},"results":[]}`)
				return
			}
			fmt.Fprint(w, `{"pagination":{"next":0},"results":[{"slug":"hearthport","launch_url":"http://h"}]}`)
		default:
			http.Error(w, "forbidden", http.StatusForbidden)
		}
	}))
	defer srv.Close()
	r := &Report{}
	ProbeAuthentik(context.Background(), AuthentikConfig{AppSlug: "hearthport", TestUsers: []string{"alice=5", "bad=x"}}, NewClient(srv.URL, nil, defaultTimeout), r)
	if got := find(r, "Hearthport access"); len(got) != 1 || got[0].Level != Pass {
		t.Fatalf("expected alice to pass via pk 5, got %+v", got)
	}
	if got := find(r, "user lookup"); len(got) != 1 || got[0].Level != Fail || !strings.Contains(got[0].Detail, "not a number") {
		t.Fatalf("expected a FAIL for the non-numeric pk, got %+v", got)
	}
}
