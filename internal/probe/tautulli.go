package probe

import (
	"context"
	"net/url"
	"strings"
)

// TautulliConfig holds the settings for the Tautulli checks.
type TautulliConfig struct {
	URL    string
	APIKey string
}

type tautulliResp[T any] struct {
	Response struct {
		Result  string `json:"result"`
		Message string `json:"message"`
		Data    T      `json:"data"`
	} `json:"response"`
}

// ProbeTautulli checks whether Tautulli can supply each Plex user's shared
// libraries, an alternative to plex.tv's shared_servers endpoint.
func ProbeTautulli(ctx context.Context, cfg TautulliConfig, r *Report) {
	const svc = "Tautulli"
	c := NewClient(strings.TrimRight(cfg.URL, "/"), nil, defaultTimeout)
	call := func(cmd string, v any) error {
		return c.GetJSON(ctx, "/api/v2", url.Values{"apikey": {cfg.APIKey}, "cmd": {cmd}}, v)
	}

	var info tautulliResp[struct {
		Version string `json:"tautulli_version"`
	}]
	if err := call("get_tautulli_info", &info); err != nil || info.Response.Result != "success" {
		r.Add(svc, "API key", Fail, "get_tautulli_info: %v %s", err, info.Response.Message)
		return
	}
	r.Add(svc, "API key", Pass, "accepted; Tautulli %s", info.Response.Data.Version)

	var users tautulliResp[[]struct {
		UserID          int    `json:"user_id"`
		Username        string `json:"username"`
		Email           string `json:"email"`
		IsActive        int    `json:"is_active"`
		SharedLibraries []any  `json:"shared_libraries"`
	}]
	if err := call("get_users", &users); err != nil || users.Response.Result != "success" {
		r.Add(svc, "users", Fail, "get_users: %v %s", err, users.Response.Message)
		return
	}
	withLibs, withEmail := 0, 0
	for _, u := range users.Response.Data {
		if len(u.SharedLibraries) > 0 {
			withLibs++
		}
		if u.Email != "" {
			withEmail++
		}
	}
	r.Add(svc, "users", Pass, "%d users; %d have shared_libraries, %d have an email for matching", len(users.Response.Data), withLibs, withEmail)
}
