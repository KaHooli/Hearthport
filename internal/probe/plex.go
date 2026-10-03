package probe

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/url"
	"strings"
)

// PlexConfig holds the settings for the Plex checks. Token is the server
// owner's X-Plex-Token; it is sent only to the Plex server and plex.tv.
type PlexConfig struct {
	URL      string
	Token    string
	PlexTV   string // defaults to https://plex.tv; overridable for tests
	MaxItems int
}

type plexContainer struct {
	MediaContainer struct {
		MachineIdentifier string `json:"machineIdentifier"`
		Version           string `json:"version"`
		Directory         []struct {
			Key   string `json:"key"`
			Title string `json:"title"`
			Type  string `json:"type"`
		} `json:"Directory"`
		Metadata []struct {
			Title            string `json:"title"`
			GrandparentTitle string `json:"grandparentTitle"`
			ParentTitle      string `json:"parentTitle"`
			Type             string `json:"type"`
			Thumb            string `json:"thumb"`
			AddedAt          int64  `json:"addedAt"`
		} `json:"Metadata"`
	} `json:"MediaContainer"`
}

type plexSharedServers struct {
	XMLName       xml.Name `xml:"MediaContainer"`
	SharedServers []struct {
		UserID   string `xml:"userID,attr"`
		Username string `xml:"username,attr"`
		Email    string `xml:"email,attr"`
		AllLibs  string `xml:"allLibraries,attr"`
		Sections []struct {
			Key    string `xml:"key,attr"`
			Title  string `xml:"title,attr"`
			Shared string `xml:"shared,attr"`
		} `xml:"Section"`
	} `xml:"SharedServer"`
}

// ProbePlex checks library sections and recently added on the server, and
// whether plex.tv reports which sections are shared with each user (the
// piece Hearthport needs to filter Plex results per user).
func ProbePlex(ctx context.Context, cfg PlexConfig, r *Report) {
	const svc = "Plex"
	hdr := http.Header{
		"X-Plex-Token":             {cfg.Token},
		"X-Plex-Client-Identifier": {"hearthport-probe"},
		"X-Plex-Product":           {"Hearthport probe"},
		"Accept":                   {"application/json"},
	}
	c := NewClient(strings.TrimRight(cfg.URL, "/"), hdr, defaultTimeout)
	if cfg.MaxItems == 0 {
		cfg.MaxItems = 5
	}

	var id plexContainer
	if err := c.GetJSON(ctx, "/identity", nil, &id); err != nil {
		r.Add(svc, "server", Fail, "%v", err)
		return
	}
	machineID := id.MediaContainer.MachineIdentifier
	r.Add(svc, "server", Info, "version %s", id.MediaContainer.Version)

	var secs plexContainer
	if err := c.GetJSON(ctx, "/library/sections", nil, &secs); err != nil {
		r.Add(svc, "token", Fail, "listing library sections: %v", err)
		return
	}
	titles := make([]string, 0, len(secs.MediaContainer.Directory))
	for _, d := range secs.MediaContainer.Directory {
		titles = append(titles, d.Title+" ("+d.Type+", key "+d.Key+")")
	}
	r.Add(svc, "library sections", Pass, "%d sections: %s", len(titles), strings.Join(titles, "; "))

	for _, d := range secs.MediaContainer.Directory {
		var ra plexContainer
		q := url.Values{"X-Plex-Container-Start": {"0"}, "X-Plex-Container-Size": {itoa(cfg.MaxItems)}}
		if err := c.GetJSON(ctx, "/library/sections/"+d.Key+"/recentlyAdded", q, &ra); err != nil {
			r.Add(svc, "recently added", Fail, "%s: %v", d.Title, err)
			continue
		}
		names := []string{}
		thumbs := 0
		for _, m := range ra.MediaContainer.Metadata {
			n := m.Title
			if m.GrandparentTitle != "" {
				n = m.GrandparentTitle + " – " + m.Title
			}
			names = append(names, n)
			if m.Thumb != "" {
				thumbs++
			}
		}
		r.Add(svc, "recently added", Pass, "%s: %d items (%d with artwork): %s", d.Title, len(names), thumbs, strings.Join(names, ", "))
	}

	plexTV := cfg.PlexTV
	if plexTV == "" {
		plexTV = "https://plex.tv"
	}
	tvHdr := http.Header{"X-Plex-Token": {cfg.Token}, "X-Plex-Client-Identifier": {"hearthport-probe"}, "Accept": {"application/xml"}}
	tv := NewClient(plexTV, tvHdr, defaultTimeout)
	var shared plexSharedServers
	if err := tv.GetXML(ctx, "/api/servers/"+url.PathEscape(machineID)+"/shared_servers", nil, &shared); err != nil {
		r.Add(svc, "per-user sharing", Fail, "plex.tv shared_servers: %v (Hearthport would fall back to group-based visibility per section)", err)
		return
	}
	if len(shared.SharedServers) == 0 {
		r.Add(svc, "per-user sharing", Warn, "plex.tv lists no shared users for this server")
		return
	}
	lines := []string{}
	for _, s := range shared.SharedServers {
		n := 0
		for _, sec := range s.Sections {
			if sec.Shared == "1" {
				n++
			}
		}
		who := s.Username
		if who == "" {
			who = s.Email
		}
		all := ""
		if s.AllLibs == "1" {
			all = " (all libraries)"
		}
		lines = append(lines, who+": "+itoa(n)+"/"+itoa(len(s.Sections))+all)
	}
	r.Add(svc, "per-user sharing", Pass, "plex.tv reports shared sections for %d users: %s", len(shared.SharedServers), strings.Join(lines, "; "))
}
