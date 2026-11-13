package probe

import (
	"context"
	"fmt"
	"strings"
)

type radarrMovie struct {
	ID              int    `json:"id"`
	Title           string `json:"title"`
	Year            int    `json:"year"`
	InCinemas       string `json:"inCinemas"`
	DigitalRelease  string `json:"digitalRelease"`
	PhysicalRelease string `json:"physicalRelease"`
	HasFile         bool   `json:"hasFile"`
	Path            string `json:"path"`
	RootFolderPath  string `json:"rootFolderPath"`
	Tags            []int  `json:"tags"`
}

// ProbeRadarr checks that Radarr's calendar returns upcoming movies with the
// release dates Hearthport's Radarr calendar plugin turns into all-day events
// (in cinemas, digital and physical), and the root folder used for category
// mapping.
func ProbeRadarr(ctx context.Context, cfg ArrConfig, r *Report) {
	const svc = "Radarr"
	c, roots := arrSetup(ctx, svc, cfg, r)
	if c == nil {
		return
	}
	q, start, end := calendarWindow(cfg, nil)
	var movies []radarrMovie
	if err := c.GetJSON(ctx, "/calendar", q, &movies); err != nil {
		r.Add(svc, "calendar", Fail, "%v", err)
		return
	}
	days := int(end.Sub(start).Hours()/24 + 0.5)
	if len(movies) == 0 {
		r.Add(svc, "calendar", Warn, "no monitored movies with a release in the %d days from %s; nothing to check", days, start.Format("2006-01-02"))
		return
	}
	var cinema, digital, physical, none, unmapped, downloaded int
	for _, m := range movies {
		in := 0
		if inWindow(m.InCinemas, start, end) {
			cinema++
			in++
		}
		if inWindow(m.DigitalRelease, start, end) {
			digital++
			in++
		}
		if inWindow(m.PhysicalRelease, start, end) {
			physical++
			in++
		}
		if in == 0 {
			none++
		}
		if m.HasFile {
			downloaded++
		}
		if m.RootFolderPath == "" && rootOf(m.Path, roots) == "" {
			unmapped++
		}
	}
	r.Add(svc, "calendar", Pass, "%d movies in %d days (%d downloaded)", len(movies), days, downloaded)

	first := movies[0]
	var dates []string
	for _, d := range [][2]string{{"cinemas", first.InCinemas}, {"digital", first.DigitalRelease}, {"physical", first.PhysicalRelease}} {
		if d[1] != "" {
			dates = append(dates, d[0]+" "+d[1])
		}
	}
	r.Add(svc, "example event", Info, "%s", fmt.Sprintf("%q (%d): %s", first.Title, first.Year, strings.Join(dates, ", ")))

	level := Pass
	if none > 0 {
		level = Warn
	}
	r.Add(svc, "release dates", level, "in the window: %d in cinemas, %d digital, %d physical; %d movies with no release date in the window", cinema, digital, physical, none)
	if len(roots) > 0 {
		level = Pass
		if unmapped > 0 {
			level = Warn
		}
		r.Add(svc, "root folder mapping", level, "%d of %d movies are outside every root folder", unmapped, len(movies))
	}
}
