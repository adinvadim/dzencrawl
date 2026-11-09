package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	crawlconfig "github.com/openclaw/crawlkit/config"
	"github.com/openclaw/crawlkit/embed"
)

type Config struct {
	DBPath      string           `toml:"db_path"`
	MarkdownDir string           `toml:"markdown_dir"`
	SessionsDir string           `toml:"sessions_dir"`
	Dzen        DzenConfig       `toml:"dzen"`
	Captcha     CaptchaConfig    `toml:"captcha"`
	Embeddings  EmbeddingsConfig `toml:"embeddings"`
}

// CaptchaConfig holds only the route to the solver key, never the key itself.
type CaptchaConfig struct {
	CredentialProcess []string `toml:"credential_process"`
}

type DzenConfig struct {
	RequestDelay string `toml:"request_delay"`
	StudioDelay  string `toml:"studio_delay"`
	MaxPages     int    `toml:"max_pages"`
	UserAgent    string `toml:"user_agent"`
}

type EmbeddingsConfig struct {
	Enabled        bool   `toml:"enabled"`
	Provider       string `toml:"provider"`
	Model          string `toml:"model"`
	BaseURL        string `toml:"base_url"`
	APIKeyEnv      string `toml:"api_key_env"`
	RequestTimeout string `toml:"request_timeout"`
	MaxInputChars  int    `toml:"max_input_chars"`
}

// DefaultBase is the data root unless DZEN_HOME overrides it.
const DefaultBase = "~/.dzen"

func base() string {
	if home := strings.TrimSpace(os.Getenv("DZEN_HOME")); home != "" {
		return home
	}
	return DefaultBase
}

func app() crawlconfig.App {
	return crawlconfig.App{Name: "dzen", BaseDir: base(), LegacyBaseDir: base()}
}

func Default() Config {
	root := base()
	return Config{
		DBPath:      filepath.ToSlash(filepath.Join(root, "dzen.db")),
		MarkdownDir: filepath.ToSlash(filepath.Join(root, "articles")),
		SessionsDir: filepath.ToSlash(filepath.Join(root, "sessions")),
		Dzen:        DzenConfig{RequestDelay: "1500ms", StudioDelay: "400ms", MaxPages: 0, UserAgent: "dzen/0.1 (Googlebot-compatible; +https://github.com/adinvadim/dzencrawl)"},
		Embeddings:  EmbeddingsConfig{Enabled: false, Provider: embed.ProviderOllama, Model: embed.DefaultLocalEmbeddingModel, BaseURL: embed.DefaultOllamaBaseURL, RequestTimeout: "2m", MaxInputChars: embed.DefaultMaxInputChars},
	}
}

func DefaultPath() (string, error) { p, err := app().DefaultPaths(); return p.ConfigPath, err }

func Load(path string) (Config, error) {
	if strings.TrimSpace(path) == "" {
		path = strings.TrimSpace(os.Getenv("DZEN_CONFIG"))
	}
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return Config{}, err
		}
	}
	path = crawlconfig.ExpandHome(path)
	cfg := Default()
	if err := crawlconfig.LoadTOML(path, &cfg); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, err
	}
	if err := cfg.Resolve(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func WriteStarter(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		path = strings.TrimSpace(os.Getenv("DZEN_CONFIG"))
	}
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return "", err
		}
	}
	path = crawlconfig.ExpandHome(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return path, crawlconfig.WriteTOML(path, Default(), 0o600)
}

func (c *Config) Resolve() error {
	var err error
	c.DBPath, err = abs(c.DBPath)
	if err != nil {
		return err
	}
	c.MarkdownDir, err = abs(c.MarkdownDir)
	if err != nil {
		return err
	}
	if c.SessionsDir == "" {
		c.SessionsDir = Default().SessionsDir
	}
	c.SessionsDir, err = abs(c.SessionsDir)
	if err != nil {
		return err
	}
	if c.Dzen.StudioDelay == "" {
		c.Dzen.StudioDelay = "400ms"
	}
	if _, err := time.ParseDuration(c.Dzen.StudioDelay); err != nil {
		return fmt.Errorf("dzen studio_delay: %w", err)
	}
	if _, err := time.ParseDuration(c.Dzen.RequestDelay); err != nil {
		return fmt.Errorf("dzen request_delay: %w", err)
	}
	if c.Dzen.UserAgent == "" {
		c.Dzen.UserAgent = Default().Dzen.UserAgent
	}
	return nil
}

func abs(path string) (string, error) { return filepath.Abs(crawlconfig.ExpandHome(path)) }
func (c Config) EmbedConfig() embed.Config {
	return embed.Config{Provider: c.Embeddings.Provider, Model: c.Embeddings.Model, BaseURL: c.Embeddings.BaseURL, APIKeyEnv: c.Embeddings.APIKeyEnv, RequestTimeout: c.Embeddings.RequestTimeout, MaxInputChars: c.Embeddings.MaxInputChars, UserAgent: c.Dzen.UserAgent}
}

// MigrateLegacyHome moves the old ~/.dzencrawl root to the new location once.
// It is a no-op when DZEN_HOME is set or the new root already exists.
func MigrateLegacyHome(oldRoot, newRoot string) (bool, error) {
	oldRoot, newRoot = crawlconfig.ExpandHome(oldRoot), crawlconfig.ExpandHome(newRoot)
	if _, err := os.Stat(newRoot); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if _, err := os.Stat(oldRoot); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if err := os.Rename(oldRoot, newRoot); err != nil {
		return false, fmt.Errorf("migrate %s: %w", oldRoot, err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		from := filepath.Join(newRoot, "dzencrawl.db"+suffix)
		if _, err := os.Stat(from); err == nil {
			if err := os.Rename(from, filepath.Join(newRoot, "dzen.db"+suffix)); err != nil {
				return true, err
			}
		}
	}
	configPath := filepath.Join(newRoot, "config.toml")
	data, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return true, err
	}
	updated := strings.NewReplacer("/.dzencrawl/dzencrawl.db", "/.dzen/dzen.db", "/.dzencrawl/", "/.dzen/").Replace(string(data))
	return true, os.WriteFile(configPath, []byte(updated), 0o600)
}
