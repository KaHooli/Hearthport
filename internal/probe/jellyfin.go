package probe

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// JellyfinConfig holds the settings for the Jellyfin checks.
type JellyfinConfig struct {
	URL       string
	APIKey    string
	TestUsers []string // Jellyfin usernames
}

// ProbeJellyfin checks that "recently added" can be fetched per user with a
// single API key, and that it respects each user's library access.
func ProbeJellyfin(ctx context.Context, cfg JellyfinConfig, r *Report) {
	const svc = "Jellyfin"
	c := NewClient(strings.TrimRight(cfg.URL, "/"), http.Header{
		"Authorization": {`MediaBrowser Client="Hearthport probe", Device="probe", DeviceId="hearthport-probe", Version="0.1", Token="` + cfg.APIKey + `"`},
		"Accept":        {"application/json"},
	}, defaultTimeout)

	var info struct {
		Version    string `json:"Version"`
		ServerName string `json:"ServerName"`
	}
	if err := c.GetJSON(ctx, "/System/Info", nil, &info); err != nil {
		r.Add(svc, "API key", Fail, "%v", err)
		return
	}
	r.Add(svc, "API key", Pass, "accepted; Jellyfin %s", info.Version)

	var users []struct {
		ID     string `json:"Id"`
		Name   string `json:"Name"`
		Policy struct {
			EnableAllFolders bool     `json:"EnableAllFolders"`
			EnabledFolders   []string `json:"EnabledFolders"`
		} `json:"Policy"`
	}
	if err := c.GetJSON(ctx, "/Users", nil, &users); err != nil {
		r.Add(svc, "users", Fail, "%v", err)
		return
	}
	r.Add(svc, "users", Pass, "%d users", len(users))
	byName := map[string]int{}
	for i, u := range users {
		byName[strings.ToLower(u.Name)] = i
	}

	if len(cfg.TestUsers) == 0 {
		r.Add(svc, "recently added", Skip, "no test users given (HP_JELLYFIN_TEST_USERS)")
	}
	for _, name := range cfg.TestUsers {
		i, ok := byName[strings.ToLower(name)]
		if !ok {
			r.Add(svc, "user matching", Warn, "no Jellyfin user named %q", name)
			continue
		}
		u := users[i]
		var latest []struct {
			Name string `json:"Name"`
			Type string `json:"Type"`
		}
		if err := c.GetJSON(ctx, "/Items/Latest", url.Values{"userId": {u.ID}, "limit": {"10"}}, &latest); err != nil {
			r.Add(svc, "recently added", Fail, "%s: %v", name, err)
			continue
		}
		access := "all libraries"
		if !u.Policy.EnableAllFolders {
			access = itoa(len(u.Policy.EnabledFolders)) + " libraries"
		}
		names := make([]string, 0, len(latest))
		for _, it := range latest {
			names = append(names, it.Name)
		}
		r.Add(svc, "recently added", Pass, "%s (access: %s): %d items: %s", name, access, len(latest), strings.Join(names, ", "))
	}
}
