package probe

import (
	"context"
	"fmt"
	"net/url"
)

type sonarrEpisode struct {
	ID            int    `json:"id"`
	SeasonNumber  int    `json:"seasonNumber"`
	EpisodeNumber int    `json:"episodeNumber"`
	Title         string `json:"title"`
	AirDateUTC    string `json:"airDateUtc"`
	HasFile       bool   `json:"hasFile"`
	Series        *struct {
		Title          string `json:"title"`
		Runtime        int    `json:"runtime"`
		Path           string `json:"path"`
		RootFolderPath string `json:"rootFolderPath"`
		Tags           []int  `json:"tags"`
	} `json:"series"`
}

// ProbeSonarr checks that Sonarr's calendar returns upcoming episodes with the
// fields Hearthport's Sonarr calendar plugin needs: an air time in UTC, the
// series (for the title, runtime and category mapping) and whether the
// episode has been downloaded.
func ProbeSonarr(ctx context.Context, cfg ArrConfig, r *Report) {
	const svc = "Sonarr"
	c, roots := arrSetup(ctx, svc, cfg, r)
	if c == nil {
		return
	}
	q, start, end := calendarWindow(cfg, url.Values{"includeSeries": {"true"}})
	var eps []sonarrEpisode
	if err := c.GetJSON(ctx, "/calendar", q, &eps); err != nil {
		r.Add(svc, "calendar", Fail, "%v", err)
		return
	}
	days := int(end.Sub(start).Hours()/24 + 0.5)
	if len(eps) == 0 {
		r.Add(svc, "calendar", Warn, "no monitored episodes in the %d days from %s; nothing to check", days, start.Format("2006-01-02"))
		return
	}
	var noAir, noSeries, noRuntime, unmapped, downloaded int
	for _, e := range eps {
		if !inWindow(e.AirDateUTC, start.AddDate(0, 0, -1), end.AddDate(0, 0, 1)) {
			noAir++
		}
		if e.HasFile {
			downloaded++
		}
		if e.Series == nil {
			noSeries++
			continue
		}
		if e.Series.Runtime <= 0 {
			noRuntime++
		}
		if e.Series.RootFolderPath == "" && rootOf(e.Series.Path, roots) == "" {
			unmapped++
		}
	}
	r.Add(svc, "calendar", Pass, "%d episodes in %d days (%d downloaded)", len(eps), days, downloaded)

	first := eps[0]
	example := fmt.Sprintf("S%02dE%02d %q at %s", first.SeasonNumber, first.EpisodeNumber, first.Title, first.AirDateUTC)
	if first.Series != nil {
		example = fmt.Sprintf("%s %s", first.Series.Title, example)
	}
	r.Add(svc, "example event", Info, "%s", example)

	level := Pass
	if noAir > 0 || noSeries > 0 {
		level = Fail
	}
	r.Add(svc, "event fields", level, "%d without a usable airDateUtc, %d without series details (includeSeries), %d series without a runtime", noAir, noSeries, noRuntime)
	if len(roots) > 0 {
		level = Pass
		if unmapped > 0 {
			level = Warn
		}
		r.Add(svc, "root folder mapping", level, "%d of %d episodes belong to a series outside every root folder", unmapped, len(eps))
	}
}
