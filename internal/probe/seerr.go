package probe

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// SeerrConfig holds the settings for the Seerr (Overseerr/Jellyseerr) checks.
type SeerrConfig struct {
	URL        string
	APIKey     string
	TestEmails []string // Hearthport users' emails, matched to Seerr users
}

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

		asUser := NewClient(base, http.Header{"X-Api-Key": {cfg.APIKey}, "X-Api-User": {strconv.Itoa(u.ID)}, "Accept": {"application/json"}}, defaultTimeout)
		var reqs struct {
			PageInfo struct {
				Results int `json:"results"`
			} `json:"pageInfo"`
			Results []seerrRequest `json:"results"`
		}
		if err := asUser.GetJSON(ctx, "/request", url.Values{"take": {"20"}, "sort": {"added"}, "requestedBy": {strconv.Itoa(u.ID)}}, &reqs); err != nil {
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
