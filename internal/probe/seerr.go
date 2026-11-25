package probe

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// SeerrConfig holds the settings for the Seerr (Overseerr/Jellyseerr) checks.
type SeerrConfig struct {
	URL        string
	APIKey     string
	TestEmails []string // Hearthport users' emails, matched to Seerr users
	// RequestTimeout limits the request-list calls, which Seerr can be slow
	// to answer because it checks Radarr and Sonarr while building them.
	// Defaults to seerrRequestTimeout.
	RequestTimeout time.Duration
}

const (
	seerrRequestTimeout = 90 * time.Second
	// seerrSlow is when a request list is slow enough that Hearthport must
	// load it after the page and cache it.
	seerrSlow = 5 * time.Second
)

type seerrUser struct {
	ID               int    `json:"id"`
	Email            string `json:"email"`
	Username         string `json:"username"`
	PlexUsername     string `json:"plexUsername"`
	JellyfinUsername string `json:"jellyfinUsername"`
	UserType         int    `json:"userType"`
}

type seerrRequest struct {
	ID          int    `json:"id"`
	Status      int    `json:"status"`
	Type        string `json:"type"`
	RequestedBy struct {
		ID int `json:"id"`
	} `json:"requestedBy"`
	Media struct {
		TMDBID int `json:"tmdbId"`
		Status int `json:"status"`
	} `json:"media"`
}

// ProbeSeerr checks user matching by email and that requests can be listed
// per user with Seerr itself enforcing the scope (X-API-User).
func ProbeSeerr(ctx context.Context, cfg SeerrConfig, r *Report) {
	const svc = "Seerr"
	base := strings.TrimRight(cfg.URL, "/") + "/api/v1"
	admin := NewClient(base, http.Header{"X-Api-Key": {cfg.APIKey}, "Accept": {"application/json"}}, defaultTimeout)

	var st struct {
		Version   string `json:"version"`
		CommitTag string `json:"commitTag"`
	}
	if err := admin.GetJSON(ctx, "/status", nil, &st); err != nil {
		r.Add(svc, "status", Fail, "%v", err)
		return
	}
	r.Add(svc, "status", Info, "version %s (%s)", st.Version, short(st.CommitTag))

	var users struct {
		PageInfo struct {
			Results int `json:"results"`
		} `json:"pageInfo"`
		Results []seerrUser `json:"results"`
	}
	if err := admin.GetJSON(ctx, "/user", url.Values{"take": {"1"}}, &users); err != nil {
		r.Add(svc, "API key", Fail, "%v", err)
		return
	}
	r.Add(svc, "API key", Pass, "accepted; %d Seerr users", users.PageInfo.Results)

	if len(cfg.TestEmails) == 0 {
		r.Add(svc, "user matching", Skip, "no test emails given (HP_SEERR_TEST_EMAILS)")
	}
	for _, email := range cfg.TestEmails {
		if isPlaceholderEmail(email) {
			r.Add(svc, "user matching", Warn, "%s looks like a placeholder; put a real user's email in HP_SEERR_TEST_EMAILS", email)
			continue
		}
		var found struct {
			Results []seerrUser `json:"results"`
		}
		if err := admin.GetJSON(ctx, "/user", url.Values{"q": {email}, "take": {"50"}}, &found); err != nil {
			r.Add(svc, "user matching", Fail, "%s: %v", email, err)
			continue
		}
		// q is a substring search over username, email, plexUsername and
		// jellyfinUsername, so only an exact, case-insensitive email match counts.
		var exact []seerrUser
		for _, u := range found.Results {
			if strings.EqualFold(u.Email, email) {
				exact = append(exact, u)
			}
		}
		if len(exact) != 1 {
			r.Add(svc, "user matching", Warn, "%s: %d exact matches (%d substring matches). Users imported from Jellyfin have their username as email until they set one", email, len(exact), len(found.Results))
			continue
		}
		u := exact[0]
		r.Add(svc, "user matching", Pass, "%s → Seerr user %d (%s)", email, u.ID, seerrUserType(u.UserType))

		timeout := cfg.RequestTimeout
		if timeout == 0 {
			timeout = seerrRequestTimeout
		}
		r.Step("Seerr: listing requests for %s (each call waits up to %s)…", email, timeout)
		asUser := NewClient(base, http.Header{"X-Api-Key": {cfg.APIKey}, "X-Api-User": {strconv.Itoa(u.ID)}, "Accept": {"application/json"}}, timeout)
		type requestPage struct {
			PageInfo struct {
				Results int `json:"results"`
			} `json:"pageInfo"`
			Results []seerrRequest `json:"results"`
		}
		// Time one request first, then twenty, to tell a fixed delay (such as
		// Seerr waiting on Radarr or Sonarr) from a cost per request.
		listQuery := func(take string) url.Values {
			return url.Values{"take": {take}, "sort": {"added"}, "requestedBy": {strconv.Itoa(u.ID)}}
		}
		var one requestPage
		start := time.Now()
		errOne := asUser.GetJSON(ctx, "/request", listQuery("1"), &one)
		tookOne := time.Since(start)
		var reqs requestPage
		start = time.Now()
		err := asUser.GetJSON(ctx, "/request", listQuery("20"), &reqs)
		took := time.Since(start)
		speed := Pass
		if took > seerrSlow || errOne != nil || err != nil {
			speed = Warn
		}
		r.Add(svc, "request list speed", speed, "%s: 1 request in %s%s, 20 requests in %s (limit %s)",
			email, tookOne.Round(100*time.Millisecond), errNote(errOne), took.Round(100*time.Millisecond), timeout)
		if err != nil {
			r.Add(svc, "own requests", Fail, "%s: %v", email, err)
			continue
		}
		foreign := 0
		for _, q := range reqs.Results {
			if q.RequestedBy.ID != u.ID {
				foreign++
			}
		}
		level := Pass
		if foreign > 0 {
			level = Fail
		}
		r.Add(svc, "own requests", level, "%s: %d requests in total, %d returned, %d belonging to someone else", email, reqs.PageInfo.Results, len(reqs.Results), foreign)

		// Titles and posters are not in the request list; check one lookup works.
		if len(reqs.Results) > 0 {
			q := reqs.Results[0]
			var meta struct {
				Title      string `json:"title"`
				Name       string `json:"name"`
				PosterPath string `json:"posterPath"`
			}
			path := fmt.Sprintf("/%s/%d", q.Type, q.Media.TMDBID)
			if err := asUser.GetJSON(ctx, path, nil, &meta); err != nil {
				r.Add(svc, "request details", Warn, "%s failed: %v", path, err)
			} else {
				r.Add(svc, "request details", Pass, "%s → %q (poster: %t)", path, meta.Title+meta.Name, meta.PosterPath != "")
			}
		}
	}
}

// isPlaceholderEmail spots the documentation example domains, which the
// probe's own instructions use, so a copied example doesn't look like a real
// miss. example.test is left alone: the throwaway test stack uses it.
func isPlaceholderEmail(email string) bool {
	_, domain, _ := strings.Cut(strings.ToLower(email), "@")
	switch domain {
	case "example.com", "example.org", "example.net":
		return true
	}
	return false
}

func errNote(err error) string {
	if err == nil {
		return ""
	}
	return " (failed: " + err.Error() + ")"
}

func seerrUserType(t int) string {
	switch t {
	case 1:
		return "Plex user"
	case 2:
		return "local user"
	case 3:
		return "Jellyfin user"
	case 4:
		return "Emby user"
	}
	return fmt.Sprintf("type %d", t)
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
