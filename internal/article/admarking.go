package article

import (
	"regexp"
	"strings"
)

var (
	adMarkingStart = regexp.MustCompile(`(?i)^реклама([\s.,:]|$)`)
	adMarkingID    = regexp.MustCompile(`(?i)(инн|erid)`)
)

// AdMarking returns the legal advertising disclosure lines of an article
// body: a line that starts with «Реклама» and names the advertiser's ИНН or
// an erid token. A body without such a line is not marked as advertising.
func AdMarking(body string) []string {
	lines := []string{}
	for _, line := range strings.Split(body, "\n") {
		text := strings.Trim(strings.ReplaceAll(line, `\`, ""), " \t*_>")
		if adMarkingStart.MatchString(text) && adMarkingID.MatchString(text) {
			lines = append(lines, text)
		}
	}
	return lines
}
