// Package session owns the cookies of each Dzen account. Nothing else stores
// them, and they never reach the archive or normal config.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/adinvadim/dzencrawl/internal/apperr"
	"github.com/adinvadim/dzencrawl/internal/article"
)

// Cookie is one browser cookie.
type Cookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
}

// Source records where a session came from, for silent re-import.
type Source struct {
	Kind           string `json:"kind"`
	BrowserProfile string `json:"browser_profile,omitempty"`
}

// Session is a verified account session.
type Session struct {
	Version     int       `json:"version"`
	Profile     string    `json:"profile"`
	Login       string    `json:"login"`
	PublisherID string    `json:"publisher_id"`
	Source      Source    `json:"source"`
	Cookies     []Cookie  `json:"cookies"`
	UserAgent   string    `json:"user_agent"`
	SavedAt     time.Time `json:"saved_at"`
}

// DefaultUserAgent is used when the source browser's agent is unknown.
const DefaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/157.0.0.0 Safari/537.36"

func relevant(domain string) bool {
	d := strings.TrimPrefix(domain, ".")
	return d == "dzen.ru" || strings.HasSuffix(d, ".dzen.ru") || d == "yandex.ru" || strings.HasSuffix(d, ".yandex.ru")
}

func rank(domain string) int {
	switch d := strings.TrimPrefix(domain, "."); {
	case d == "dzen.ru":
		return 0
	case strings.HasSuffix(d, ".dzen.ru"):
		return 1
	case d == "yandex.ru":
		return 2
	}
	return 3
}

// Compact keeps one value per cookie name on dzen.ru and yandex.ru domains.
// Sending every stored variant makes Dzen answer HTTP 431.
func Compact(cookies []Cookie) []Cookie {
	best := map[string]Cookie{}
	for _, c := range cookies {
		if !relevant(c.Domain) || c.Name == "" || c.Value == "" {
			continue
		}
		if prior, ok := best[c.Name]; !ok || rank(c.Domain) < rank(prior.Domain) {
			best[c.Name] = c
		}
	}
	out := make([]Cookie, 0, len(best))
	for _, c := range best {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Header renders the Cookie request header.
func Header(cookies []Cookie) string {
	parts := make([]string, 0, len(cookies))
	for _, c := range cookies {
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

// Store keeps one private directory per profile.
type Store struct{ Dir string }

var profileName = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

// ValidProfile reports whether a profile name is safe as a directory.
func ValidProfile(name string) bool {
	return profileName.MatchString(name) && name != "." && name != ".." && !strings.Contains(name, "..")
}

func (s Store) path(profile string) string { return filepath.Join(s.Dir, profile, "session.json") }

// Save atomically promotes a session; the previous one survives any failure.
func (s Store) Save(sess Session) error {
	if !ValidProfile(sess.Profile) {
		return apperr.New("usage_error", "invalid profile name %q", sess.Profile)
	}
	if err := os.MkdirAll(filepath.Join(s.Dir, sess.Profile), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(s.Dir, 0o700); err != nil {
		return err
	}
	sess.Version = 1
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	return article.AtomicWrite(s.path(sess.Profile), data, 0o600)
}

// Load reads a profile's session.
func (s Store) Load(profile string) (Session, error) {
	var out Session
	if !ValidProfile(profile) {
		return out, apperr.New("usage_error", "invalid profile name %q", profile)
	}
	data, err := os.ReadFile(s.path(profile))
	if errors.Is(err, os.ErrNotExist) {
		return out, apperr.New("not_authenticated", "no session for profile %q; run: dzen auth import", profile)
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, apperr.New("session_invalid", "session file for %q is corrupt", profile)
	}
	return out, nil
}

// List returns the names of all saved profiles.
func (s Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			if _, err := os.Stat(s.path(e.Name())); err == nil {
				out = append(out, e.Name())
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// Current returns the selected profile. With exactly one profile it needs no
// selection.
func (s Store) Current() (string, error) {
	data, err := os.ReadFile(filepath.Join(s.Dir, "current"))
	if err == nil {
		if name := strings.TrimSpace(string(data)); name != "" {
			return name, nil
		}
	}
	profiles, err := s.List()
	if err != nil {
		return "", err
	}
	switch len(profiles) {
	case 0:
		return "", apperr.New("not_authenticated", "no accounts yet; run: dzen auth import")
	case 1:
		return profiles[0], nil
	}
	return "", apperr.New("usage_error", "several accounts exist; pass --profile or run: dzen auth use NAME")
}

// Use selects the default profile.
func (s Store) Use(profile string) error {
	if _, err := s.Load(profile); err != nil {
		return err
	}
	return article.AtomicWrite(filepath.Join(s.Dir, "current"), []byte(profile+"\n"), 0o600)
}

// Delete removes a profile and its cookies.
func (s Store) Delete(profile string) error {
	if !ValidProfile(profile) {
		return apperr.New("usage_error", "invalid profile name %q", profile)
	}
	if _, err := s.Load(profile); err != nil {
		return err
	}
	if current, err := os.ReadFile(filepath.Join(s.Dir, "current")); err == nil && strings.TrimSpace(string(current)) == profile {
		_ = os.Remove(filepath.Join(s.Dir, "current"))
	}
	if err := os.RemoveAll(filepath.Join(s.Dir, profile)); err != nil {
		return fmt.Errorf("remove profile: %w", err)
	}
	return nil
}
