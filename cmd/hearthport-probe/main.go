// Command hearthport-probe runs read-only checks against Authentik, Seerr,
// Jellyfin, Plex and Tautulli, and prints a Markdown report. It never changes
// anything on those servers: every request it makes is an HTTP GET.
//
// Configure it with environment variables; any service whose URL is unset is
// skipped. See docs/spike/phase0.md for how to run it against your own servers.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kahooli/hearthport/internal/probe"
)

func main() {
	redact := flag.Bool("redact", true, "mask email addresses in the report")
	timeout := flag.Duration("timeout", 2*time.Minute, "overall time limit")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: hearthport-probe [-redact=false] [-timeout=2m]")
		fmt.Fprintln(os.Stderr, "\nEnvironment (services with no URL are skipped):")
		for _, e := range envHelp {
			fmt.Fprintf(os.Stderr, "  %-26s %s\n", e[0], e[1])
		}
	}
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	r := &probe.Report{Redact: *redact}

	if u := env("HP_AUTHENTIK_URL"); u != "" {
		probe.ProbeAuthentik(ctx, probe.AuthentikConfig{
			URL:       u,
			Token:     env("HP_AUTHENTIK_TOKEN"),
			AppSlug:   envOr("HP_AUTHENTIK_APP_SLUG", "hearthport"),
			TestUsers: list("HP_AUTHENTIK_TEST_USERS"),
		}, nil, r)
	}
	if u := env("HP_SEERR_URL"); u != "" {
		probe.ProbeSeerr(ctx, probe.SeerrConfig{URL: u, APIKey: env("HP_SEERR_API_KEY"), TestEmails: list("HP_SEERR_TEST_EMAILS")}, r)
	}
	if u := env("HP_JELLYFIN_URL"); u != "" {
		probe.ProbeJellyfin(ctx, probe.JellyfinConfig{URL: u, APIKey: env("HP_JELLYFIN_API_KEY"), TestUsers: list("HP_JELLYFIN_TEST_USERS")}, r)
	}
	if u := env("HP_PLEX_URL"); u != "" {
		probe.ProbePlex(ctx, probe.PlexConfig{URL: u, Token: env("HP_PLEX_TOKEN")}, r)
	}
	if u := env("HP_TAUTULLI_URL"); u != "" {
		probe.ProbeTautulli(ctx, probe.TautulliConfig{URL: u, APIKey: env("HP_TAUTULLI_API_KEY")}, r)
	}

	if len(r.Results) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	r.WriteMarkdown(os.Stdout)
	if r.Counts()[probe.Fail] > 0 {
		os.Exit(1)
	}
}

var envHelp = [][2]string{
	{"HP_AUTHENTIK_URL", "e.g. https://auth.example.com"},
	{"HP_AUTHENTIK_TOKEN", "service-account API token"},
	{"HP_AUTHENTIK_APP_SLUG", "Hearthport's application slug (default hearthport)"},
	{"HP_AUTHENTIK_TEST_USERS", "comma-separated usernames to check"},
	{"HP_SEERR_URL", "e.g. https://seerr.example.com"},
	{"HP_SEERR_API_KEY", "Seerr API key (Settings → General)"},
	{"HP_SEERR_TEST_EMAILS", "comma-separated user emails to match"},
	{"HP_JELLYFIN_URL", "e.g. https://jellyfin.example.com"},
	{"HP_JELLYFIN_API_KEY", "Jellyfin API key"},
	{"HP_JELLYFIN_TEST_USERS", "comma-separated Jellyfin usernames"},
	{"HP_PLEX_URL", "e.g. http://plex.lan:32400"},
	{"HP_PLEX_TOKEN", "server owner's X-Plex-Token"},
	{"HP_TAUTULLI_URL", "e.g. http://tautulli.lan:8181"},
	{"HP_TAUTULLI_API_KEY", "Tautulli API key"},
}

func env(k string) string { return strings.TrimSpace(os.Getenv(k)) }

func envOr(k, def string) string {
	if v := env(k); v != "" {
		return v
	}
	return def
}

func list(k string) []string {
	var out []string
	for _, s := range strings.Split(env(k), ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
