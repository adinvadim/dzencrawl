package crawl

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// The archive runs unattended with a live session, so it must not be able to
// reach any code that writes to Dzen.
func TestCrawlPackageCannotImportWriteCode(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(path, "internal/mutate") || strings.Contains(path, "internal/studio") {
				t.Errorf("%s imports %s", name, path)
			}
		}
	}
}
