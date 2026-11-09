package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateLegacyHomeMovesDataAndRewritesConfig(t *testing.T) {
	root := t.TempDir()
	oldRoot, newRoot := filepath.Join(root, ".dzencrawl"), filepath.Join(root, ".dzen")
	if err := os.MkdirAll(oldRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(oldRoot, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("dzencrawl.db", "db")
	write("dzencrawl.db-wal", "wal")
	write("config.toml", "db_path = \"~/.dzencrawl/dzencrawl.db\"\nmarkdown_dir = \"~/.dzencrawl/articles\"\n")
	moved, err := MigrateLegacyHome(oldRoot, newRoot)
	if err != nil || !moved {
		t.Fatal(moved, err)
	}
	if _, err := os.Stat(oldRoot); !os.IsNotExist(err) {
		t.Fatal("old root still exists")
	}
	for _, name := range []string{"dzen.db", "dzen.db-wal"} {
		if _, err := os.Stat(filepath.Join(newRoot, name)); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(newRoot, "config.toml"))
	if strings.Contains(string(data), "dzencrawl") || !strings.Contains(string(data), "~/.dzen/dzen.db") {
		t.Fatalf("%s", data)
	}
	if moved, err := MigrateLegacyHome(oldRoot, newRoot); err != nil || moved {
		t.Fatal(moved, err)
	}
}

func TestMigrateLeavesAnExistingNewRootAlone(t *testing.T) {
	root := t.TempDir()
	oldRoot, newRoot := filepath.Join(root, ".dzencrawl"), filepath.Join(root, ".dzen")
	for _, dir := range []string{oldRoot, newRoot} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if moved, err := MigrateLegacyHome(oldRoot, newRoot); err != nil || moved {
		t.Fatal(moved, err)
	}
	if _, err := os.Stat(oldRoot); err != nil {
		t.Fatal("old root was touched")
	}
}

func TestLoadIgnoresLegacyFirecrawlSection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DZEN_HOME", home)
	t.Setenv("DZEN_CONFIG", "")
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[firecrawl]\nbase_url = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cfg.SessionsDir, "/sessions") {
		t.Fatal(cfg.SessionsDir)
	}
}
