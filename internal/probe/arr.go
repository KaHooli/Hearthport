package probe

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ArrConfig holds the settings for a Sonarr or Radarr check.
type ArrConfig struct {
	URL    string
	APIKey string
	Days   int // how far ahead to read the calendar (default 30)
}

// calendarBack is how far into the past the calendar checks read, matching
// the past window of Hearthport's planned calendar feed (PLAN.md §5.5).
const calendarBack = 7 * 24 * time.Hour

type arrRootFolder struct {
	Path string `json:"path"`
}

type arrTag struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
}

// arrSetup checks the API key and reads the root folders and tags that
// Hearthport would use to map calendar events to categories. It returns the
// client and the root folder paths, or nil if the API key check failed.
func arrSetup(ctx context.Context, svc string, cfg ArrConfig, r *Report) (*Client, []string) {
	c := NewClient(strings.TrimRight(cfg.URL, "/")+"/api/v3", http.Header{"X-Api-Key": {cfg.APIKey}, "Accept": {"application/json"}}, defaultTimeout)

	var st struct {
		AppName string `json:"appName"`
		Version string `json:"version"`
	}
	if err := c.GetJSON(ctx, "/system/status", nil, &st); err != nil {
		r.Add(svc, "API key", Fail, "%v", err)
		return nil, nil
	}
	r.Add(svc, "API key", Pass, "accepted; %s %s", st.AppName, st.Version)

	var roots []arrRootFolder
	var paths []string
	if err := c.GetJSON(ctx, "/rootfolder", nil, &roots); err != nil {
		r.Add(svc, "root folders", Warn, "%v (category mapping by root folder won't work)", err)
	} else {
		for _, f := range roots {
			paths = append(paths, f.Path)
		}
		r.Add(svc, "root folders", Info, "%d: %s", len(paths), strings.Join(paths, ", "))
	}

	var tags []arrTag
	if err := c.GetJSON(ctx, "/tag", nil, &tags); err != nil {
		r.Add(svc, "tags", Warn, "%v (category mapping by tag won't work)", err)
	} else {
		labels := make([]string, 0, len(tags))
		for _, t := range tags {
			labels = append(labels, t.Label)
		}
		r.Add(svc, "tags", Info, "%d: %s", len(labels), strings.Join(labels, ", "))
	}
	return c, paths
}

// calendarWindow returns the query for the calendar endpoint and the window it covers.
func calendarWindow(cfg ArrConfig, extra url.Values) (url.Values, time.Time, time.Time) {
	days := cfg.Days
	if days <= 0 {
		days = 30
	}
	now := time.Now().UTC()
	start, end := now.Add(-calendarBack), now.AddDate(0, 0, days)
	q := url.Values{
		"start":       {start.Format(time.RFC3339)},
		"end":         {end.Format(time.RFC3339)},
		"unmonitored": {"false"},
	}
	for k, v := range extra {
		q[k] = v
	}
	return q, start, end
}

// rootOf returns the root folder that contains path, or "" if none does.
func rootOf(path string, roots []string) string {
	best := ""
	for _, root := range roots {
		r := strings.TrimRight(root, `/\`)
		if (path == r || strings.HasPrefix(path, r+"/") || strings.HasPrefix(path, r+`\`)) && len(r) > len(best) {
			best = root
		}
	}
	return best
}

// inWindow reports whether the date s (RFC 3339, may be empty) falls in [start, end].
func inWindow(s string, start, end time.Time) bool {
	if s == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, s)
	return err == nil && !t.Before(start) && !t.After(end)
}
