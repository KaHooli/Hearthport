package probe

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// AuthentikConfig holds the settings for the Authentik checks.
type AuthentikConfig struct {
	URL       string   // e.g. https://auth.example.com
	Token     string   // service-account API token
	AppSlug   string   // Hearthport's own application slug
	TestUsers []string // usernames to list applications for
}

type akApp struct {
	PK            string `json:"pk"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	Group         string `json:"group"`
	LaunchURL     string `json:"launch_url"`
	MetaLaunchURL string `json:"meta_launch_url"`
	MetaIcon      string `json:"meta_icon"`
	MetaHide      bool   `json:"meta_hide"`
	OpenInNewTab  bool   `json:"open_in_new_tab"`
	PBMUUID       string `json:"pbm_uuid"`
}

type akPage[T any] struct {
	Pagination struct {
		Next       int `json:"next"`
		Count      int `json:"count"`
		TotalPages int `json:"total_pages"`
	} `json:"pagination"`
	Results []T `json:"results"`
}

type akUser struct {
	PK          int    `json:"pk"`
	Username    string `json:"username"`
	UID         string `json:"uid"`
	IsSuperuser bool   `json:"is_superuser"`
}

// ProbeAuthentik checks that the token can discover each test user's
// applications the way Hearthport will (docs/PLAN.md §4.5, §5).
func ProbeAuthentik(ctx context.Context, cfg AuthentikConfig, c *Client, r *Report) {
	const svc = "Authentik"
	if c == nil {
		c = NewClient(strings.TrimRight(cfg.URL, "/")+"/api/v3", http.Header{
			"Authorization": {"Bearer " + cfg.Token},
			"Accept":        {"application/json"},
		}, defaultTimeout)
	}

	var ver struct {
		Current string `json:"version_current"`
	}
	if err := c.GetJSON(ctx, "/admin/version/", nil, &ver); err == nil {
		r.Add(svc, "version", Info, "%s", ver.Current)
	} else {
		r.Add(svc, "version", Info, "not readable with this token (%v); that is fine", err)
	}

	var me struct {
		User akUser `json:"user"`
	}
	if err := c.GetJSON(ctx, "/core/users/me/", nil, &me); err != nil {
		r.Add(svc, "token", Fail, "token rejected: %v", err)
		return
	}
	if me.User.IsSuperuser {
		r.Add(svc, "token", Warn, "token belongs to %q, a superuser. It works, but Hearthport only needs a service account with the hearthport-discovery role (view_user_applications, view_user)", me.User.Username)
	} else {
		r.Add(svc, "token", Pass, "token belongs to %q (not a superuser)", me.User.Username)
	}

	if len(cfg.TestUsers) == 0 {
		r.Add(svc, "per-user apps", Skip, "no test users given (HP_AUTHENTIK_TEST_USERS)")
	}
	unionVisible := map[string]int{}
	for _, username := range cfg.TestUsers {
		var users akPage[akUser]
		err := c.GetJSON(ctx, "/core/users/", url.Values{"username": {username}}, &users)
		switch {
		case StatusOf(err) == http.StatusForbidden:
			r.Add(svc, "user lookup", Fail, "403 looking up %q: the token needs authentik_core.view_user (or Hearthport must get the user's pk from a claim)", username)
			continue
		case err != nil:
			r.Add(svc, "user lookup", Fail, "looking up %q: %v", username, err)
			continue
		case len(users.Results) != 1:
			r.Add(svc, "user lookup", Fail, "%d users match username %q", len(users.Results), username)
			continue
		}
		u := users.Results[0]
		r.Add(svc, "user lookup", Pass, "%q has pk %d", username, u.PK)

		apps, pages, emptyPages, err := akAppsForUser(ctx, c, u.PK)
		if err != nil {
			if strings.Contains(err.Error(), "User not found") {
				r.Add(svc, "apps for user", Fail, "for_user=%d returned \"User not found\": the token needs authentik_core.view_user_applications", u.PK)
			} else {
				r.Add(svc, "apps for user", Fail, "%q: %v", username, err)
			}
			continue
		}
		slugs := make([]string, 0, len(apps))
		hasSelf := false
		noLaunch := 0
		for _, a := range apps {
			slugs = append(slugs, a.Slug)
			unionVisible[a.Slug]++
			if a.Slug == cfg.AppSlug {
				hasSelf = true
			}
			if a.LaunchURL == "" {
				noLaunch++
			}
		}
		sort.Strings(slugs)
		r.Add(svc, "apps for user", Pass, "%q can access %d apps (%d pages, %d empty pages, %d without a launch URL): %s",
			username, len(apps), pages, emptyPages, noLaunch, strings.Join(slugs, ", "))
		if cfg.AppSlug != "" {
			if hasSelf {
				r.Add(svc, "Hearthport access", Pass, "%q can access %q", username, cfg.AppSlug)
			} else {
				r.Add(svc, "Hearthport access", Info, "%q cannot access %q, so Hearthport would refuse them", username, cfg.AppSlug)
			}
		}
	}

	akUnboundApps(ctx, c, r)
}

// akAppsForUser follows every page. Authentik paginates before filtering by
// policy, so a page can be short or empty while later pages still have results.
func akAppsForUser(ctx context.Context, c *Client, pk int) (apps []akApp, pages, emptyPages int, err error) {
	page := 1
	for page > 0 && pages < 100 {
		var p akPage[akApp]
		q := url.Values{"for_user": {strconv.Itoa(pk)}, "page": {strconv.Itoa(page)}, "page_size": {"100"}}
		if err = c.GetJSON(ctx, "/core/applications/", q, &p); err != nil {
			return nil, pages, emptyPages, err
		}
		pages++
		if len(p.Results) == 0 {
			emptyPages++
		}
		apps = append(apps, p.Results...)
		page = p.Pagination.Next
	}
	return apps, pages, emptyPages, nil
}

// akUnboundApps lists applications with no policy, group or user bindings.
// Authentik lets every user open those (unless core_default_app_access is off).
func akUnboundApps(ctx context.Context, c *Client, r *Report) {
	const svc = "Authentik"
	var all []akApp
	page := 1
	for page > 0 {
		var p akPage[akApp]
		err := c.GetJSON(ctx, "/core/applications/", url.Values{"superuser_full_list": {"true"}, "page": {strconv.Itoa(page)}, "page_size": {"100"}}, &p)
		if err != nil {
			r.Add(svc, "apps without bindings", Skip, "could not list all applications: %v", err)
			return
		}
		all = append(all, p.Results...)
		page = p.Pagination.Next
	}
	var unbound []string
	for _, a := range all {
		var b akPage[struct{}]
		err := c.GetJSON(ctx, "/policies/bindings/", url.Values{"target": {a.PBMUUID}, "page_size": {"1"}}, &b)
		if StatusOf(err) == http.StatusForbidden {
			r.Add(svc, "apps without bindings", Skip, "token cannot read policy bindings (needs authentik_policies.view_policybinding); skipped")
			return
		}
		if err != nil {
			r.Add(svc, "apps without bindings", Skip, "reading bindings: %v", err)
			return
		}
		if b.Pagination.Count == 0 {
			unbound = append(unbound, a.Slug)
		}
	}
	if len(all) == 0 {
		r.Add(svc, "apps without bindings", Skip, "token can only see its own applications (superuser_full_list needs a superuser)")
		return
	}
	if len(unbound) > 0 {
		sort.Strings(unbound)
		r.Add(svc, "apps without bindings", Warn, "%d of %d apps have no bindings, so every Authentik user can open them: %s",
			len(unbound), len(all), strings.Join(unbound, ", "))
	} else {
		r.Add(svc, "apps without bindings", Pass, "all %d apps have at least one binding", len(all))
	}
}
