// Package probe implements read-only checks against the services Hearthport
// integrates with. It is used by cmd/hearthport-probe during the Phase 0 spike
// and is safe to run against production servers: every request is a GET.
package probe

import (
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Level is the outcome of a single check.
type Level string

const (
	Pass Level = "PASS"
	Warn Level = "WARN"
	Fail Level = "FAIL"
	Info Level = "INFO"
	Skip Level = "SKIP"
)

// Result is one line of the report.
type Result struct {
	Service string
	Check   string
	Level   Level
	Detail  string
}

// Report collects results and redacts anything that looks like personal data.
type Report struct {
	Results []Result
	Redact  bool
}

func (r *Report) Add(service, check string, level Level, format string, args ...any) {
	detail := fmt.Sprintf(format, args...)
	if r.Redact {
		detail = RedactEmails(detail)
	}
	r.Results = append(r.Results, Result{service, check, level, detail})
}

// Counts returns how many results there are at each level.
func (r *Report) Counts() map[Level]int {
	c := map[Level]int{}
	for _, res := range r.Results {
		c[res.Level]++
	}
	return c
}

// WriteMarkdown renders the report as a Markdown table, grouped by service.
func (r *Report) WriteMarkdown(w io.Writer) {
	fmt.Fprintln(w, "| Service | Check | Result | Detail |")
	fmt.Fprintln(w, "|---|---|---|---|")
	for _, res := range r.Results {
		fmt.Fprintf(w, "| %s | %s | %s | %s |\n", res.Service, res.Check, res.Level, escapeCell(res.Detail))
	}
	c := r.Counts()
	fmt.Fprintf(w, "\n%d pass, %d warn, %d fail, %d info, %d skipped\n", c[Pass], c[Warn], c[Fail], c[Info], c[Skip])
}

func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	return strings.ReplaceAll(s, "\n", "<br>")
}

var emailRe = regexp.MustCompile(`([A-Za-z0-9._%+-])[A-Za-z0-9._%+-]*@([A-Za-z0-9.-]+\.[A-Za-z]{2,})`)

// RedactEmails keeps the first character and the domain of each email address.
func RedactEmails(s string) string {
	return emailRe.ReplaceAllString(s, "$1***@$2")
}
