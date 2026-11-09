package crawl

import (
	"os/exec"
	"strings"
	"testing"
)

// The archive runs unattended with a live session, so neither this package nor
// anything it links may reach code that writes to Dzen.
func TestCrawlCannotLinkWriteCode(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasSuffix(dep, "/internal/mutate") || strings.HasSuffix(dep, "/internal/studio") {
			t.Errorf("crawl links %s", dep)
		}
	}
}
