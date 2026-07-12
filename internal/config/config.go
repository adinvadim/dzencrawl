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
	Firecrawl   FirecrawlConfig  `toml:"firecrawl"`
	Dzen        DzenConfig       `toml:"dzen"`
	Embeddings  EmbeddingsConfig `toml:"embeddings"`
}

type FirecrawlConfig struct {
	BaseURL   string `toml:"base_url"`
	APIKeyEnv string `toml:"api_key_env"`
	Timeout   string `toml:"timeout"`
}

type DzenConfig struct {
	RequestDelay string `toml:"request_delay"`
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

var app = crawlconfig.App{Name: "dzencrawl", BaseDir: "~/.dzencrawl", LegacyBaseDir: "~/.dzencrawl"}

func Default() Config {
	paths, _ := app.DefaultPaths()
	base := paths.BaseDir
	if base == "" {
		base = "~/.dzencrawl"
	}
	return Config{
		DBPath:      filepath.ToSlash(filepath.Join(base, "dzencrawl.db")),
		MarkdownDir: filepath.ToSlash(filepath.Join(base, "articles")),
		Firecrawl:   FirecrawlConfig{BaseURL: "https://api.firecrawl.dev/v2", APIKeyEnv: "FIRECRAWL_API_KEY", Timeout: "3m"},
		Dzen:        DzenConfig{RequestDelay: "1500ms", MaxPages: 0, UserAgent: "dzencrawl/0.1 (Googlebot-compatible; +https://github.com/adinvadim/dzencrawl)"},
		Embeddings:  EmbeddingsConfig{Enabled: false, Provider: embed.ProviderOllama, Model: embed.DefaultLocalEmbeddingModel, BaseURL: embed.DefaultOllamaBaseURL, RequestTimeout: "2m", MaxInputChars: embed.DefaultMaxInputChars},
	}
}

func DefaultPath() (string, error) { p, err := app.DefaultPaths(); return p.ConfigPath, err }

func Load(path string) (Config, error) {
	if strings.TrimSpace(path) == "" {
		path = strings.TrimSpace(os.Getenv("DZENCRAWL_CONFIG"))
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
		path = strings.TrimSpace(os.Getenv("DZENCRAWL_CONFIG"))
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
	if _, err := time.ParseDuration(c.Firecrawl.Timeout); err != nil {
		return fmt.Errorf("firecrawl timeout: %w", err)
	}
	if _, err := time.ParseDuration(c.Dzen.RequestDelay); err != nil {
		return fmt.Errorf("dzen request_delay: %w", err)
	}
	if c.Firecrawl.BaseURL == "" {
		c.Firecrawl.BaseURL = "https://api.firecrawl.dev/v2"
	}
	if c.Firecrawl.APIKeyEnv == "" {
		c.Firecrawl.APIKeyEnv = "FIRECRAWL_API_KEY"
	}
	if c.Dzen.UserAgent == "" {
		c.Dzen.UserAgent = "dzencrawl/0.1 (Googlebot-compatible)"
	}
	return nil
}

func abs(path string) (string, error) { return filepath.Abs(crawlconfig.ExpandHome(path)) }
func (c Config) FirecrawlKey() string { return strings.TrimSpace(os.Getenv(c.Firecrawl.APIKeyEnv)) }
func (c Config) EmbedConfig() embed.Config {
	return embed.Config{Provider: c.Embeddings.Provider, Model: c.Embeddings.Model, BaseURL: c.Embeddings.BaseURL, APIKeyEnv: c.Embeddings.APIKeyEnv, RequestTimeout: c.Embeddings.RequestTimeout, MaxInputChars: c.Embeddings.MaxInputChars, UserAgent: c.Dzen.UserAgent}
}
