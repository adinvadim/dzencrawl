package markdown

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/adinvadim/dzencrawl/internal/dzen"
	"github.com/adinvadim/dzencrawl/internal/store"
)

func TestExportTightensPermissionsOfExistingFiles(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertArticle(context.Background(), dzen.Article{ID: "abcdef012345", Channel: "chan", Title: "Заголовок", URL: "u", Markdown: "Текст"}, nil, 1); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "articles")
	if _, err := Export(context.Background(), st, dir); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "chan", "*.md"))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	for _, path := range []string{dir, filepath.Join(dir, "chan"), files[0]} {
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Export(context.Background(), st, dir); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, "chan"): 0o700, files[0]: 0o600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("%s: %v %v", path, info.Mode(), err)
		}
	}
}
