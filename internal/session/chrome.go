package session

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	_ "modernc.org/sqlite"

	"github.com/adinvadim/dzencrawl/internal/apperr"
)

// Candidate is an unverified set of cookies found in a browser.
type Candidate struct {
	Cookies   []Cookie
	UserAgent string
	Source    Source
}

// ChromeRoot returns Chrome's user data directory.
func ChromeRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Google", "Chrome"), nil
	}
	return filepath.Join(home, ".config", "google-chrome"), nil
}

// ImportOptions selects where to look for a session.
type ImportOptions struct {
	Root           string
	BrowserProfile string
	NoInput        bool
}

// FromCDP reads cookies from a running Chrome with remote debugging enabled.
func FromCDP(ctx context.Context, root string) (Candidate, error) {
	raw, err := os.ReadFile(filepath.Join(root, "DevToolsActivePort"))
	if err != nil {
		return Candidate{}, fmt.Errorf("remote debugging is not enabled: %w", err)
	}
	lines := strings.Fields(string(raw))
	if len(lines) < 2 {
		return Candidate{}, errors.New("DevToolsActivePort is malformed")
	}
	if _, err := strconv.Atoi(lines[0]); err != nil {
		return Candidate{}, errors.New("DevToolsActivePort is malformed")
	}
	cookies, ua, err := cdpCookies(ctx, "ws://127.0.0.1:"+lines[0]+lines[1])
	if err != nil {
		return Candidate{}, err
	}
	return Candidate{Cookies: cookies, UserAgent: ua, Source: Source{Kind: "chrome"}}, nil
}

func cdpCookies(parent context.Context, endpoint string) ([]Cookie, string, error) {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(64 << 20)
	call := func(id int, method string) (json.RawMessage, error) {
		payload, _ := json.Marshal(map[string]any{"id": id, "method": method})
		if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
			return nil, err
		}
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return nil, err
			}
			var message struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(data, &message) != nil || message.ID != id {
				continue
			}
			if message.Error != nil {
				return nil, errors.New(message.Error.Message)
			}
			return message.Result, nil
		}
	}
	var version struct {
		UserAgent string `json:"userAgent"`
	}
	if raw, err := call(1, "Browser.getVersion"); err == nil {
		_ = json.Unmarshal(raw, &version)
	}
	raw, err := call(2, "Storage.getCookies")
	if err != nil {
		return nil, "", err
	}
	var result struct {
		Cookies []Cookie `json:"cookies"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, "", err
	}
	return result.Cookies, version.UserAgent, nil
}

// BrowserProfiles lists Chrome profile directories holding a Dzen session.
func BrowserProfiles(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, apperr.New("not_authenticated", "Chrome profile directory was not found")
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || (name != "Default" && !strings.HasPrefix(name, "Profile ")) {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, name, "Cookies")); err == nil {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// FromDatabase decrypts a Chrome profile's cookie store.
func FromDatabase(ctx context.Context, opts ImportOptions, profile string) (Candidate, error) {
	key, err := chromeKey(ctx, opts.NoInput)
	if err != nil {
		return Candidate{}, err
	}
	cookies, err := readCookieDB(ctx, filepath.Join(opts.Root, profile), key)
	if err != nil {
		return Candidate{}, err
	}
	return Candidate{Cookies: cookies, UserAgent: DefaultUserAgent, Source: Source{Kind: "chrome", BrowserProfile: profile}}, nil
}

// chromeKey asks /usr/bin/security for the Chrome Safe Storage password, so a
// single "Always Allow" survives rebuilds of this binary.
func chromeKey(parent context.Context, noInput bool) ([]byte, error) {
	if runtime.GOOS != "darwin" {
		return nil, apperr.New("not_authenticated", "reading the Chrome cookie database is supported on macOS only; enable Chrome remote debugging")
	}
	limit := 60 * time.Second
	if noInput {
		limit = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, limit)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-w", "-s", "Chrome Safe Storage").Output()
	if err != nil {
		return nil, apperr.New("not_authenticated", "Keychain access to Chrome Safe Storage was not granted")
	}
	return deriveKey(strings.TrimSpace(string(out)))
}

func deriveKey(password string) ([]byte, error) {
	return pbkdf2.Key(sha1.New, password, []byte("saltysalt"), 1003, 16)
}

func readCookieDB(ctx context.Context, profileDir string, key []byte) ([]Cookie, error) {
	dir, err := os.MkdirTemp("", "dzen-cookies-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	for _, name := range []string{"Cookies", "Cookies-wal", "Cookies-shm"} {
		if err := copyFile(filepath.Join(profileDir, name), filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(filepath.Join(dir, "Cookies")))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	version := 0
	_ = db.QueryRowContext(ctx, `select value from meta where key='version'`).Scan(&version)
	rows, err := db.QueryContext(ctx, `select host_key,name,encrypted_value,value from cookies where host_key like '%dzen.ru' or host_key like '%yandex.ru'`)
	if err != nil {
		return nil, fmt.Errorf("read cookie database: %w", err)
	}
	defer rows.Close()
	var out []Cookie
	for rows.Next() {
		var host, name, plain string
		var encrypted []byte
		if err := rows.Scan(&host, &name, &encrypted, &plain); err != nil {
			return nil, err
		}
		value := plain
		if len(encrypted) != 0 {
			decrypted, err := decryptCookie(encrypted, key, host, version)
			if err != nil {
				continue
			}
			value = decrypted
		}
		out = append(out, Cookie{Name: name, Value: value, Domain: host})
	}
	return out, rows.Err()
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// decryptCookie handles Chrome's macOS v10 format. Databases from version 24
// prefix the plaintext with SHA-256 of the host.
func decryptCookie(encrypted, key []byte, host string, version int) (string, error) {
	if !bytes.HasPrefix(encrypted, []byte("v10")) {
		return "", errors.New("unsupported cookie encryption")
	}
	data := encrypted[3:]
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return "", errors.New("malformed cookie value")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	iv := bytes.Repeat([]byte(" "), aes.BlockSize)
	plain := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, data)
	pad := int(plain[len(plain)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(plain) {
		return "", errors.New("bad cookie padding")
	}
	plain = plain[:len(plain)-pad]
	if version >= 24 {
		if len(plain) < 32 {
			return "", errors.New("short cookie value")
		}
		plain = plain[32:]
	}
	_ = host
	return string(plain), nil
}

// LoginWindow opens a dedicated Chrome and waits until verify accepts its
// cookies. The profile directory stays isolated from the user's browser.
func LoginWindow(ctx context.Context, userDataDir string, deadline time.Duration, verify func(Candidate) bool) (Candidate, error) {
	binary := chromeBinary()
	if binary == "" {
		return Candidate{}, apperr.New("not_authenticated", "Google Chrome was not found")
	}
	if err := os.MkdirAll(userDataDir, 0o700); err != nil {
		return Candidate{}, err
	}
	portFile := filepath.Join(userDataDir, "DevToolsActivePort")
	_ = os.Remove(portFile)
	cmd := exec.CommandContext(ctx, binary, "--user-data-dir="+userDataDir, "--remote-debugging-port=0", "--no-first-run", "--no-default-browser-check", "https://dzen.ru/")
	if err := cmd.Start(); err != nil {
		return Candidate{}, err
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		select {
		case <-ctx.Done():
			return Candidate{}, apperr.New("interrupted", "login cancelled")
		case <-time.After(2 * time.Second):
		}
		candidate, err := FromCDP(ctx, userDataDir)
		if err != nil || len(Compact(candidate.Cookies)) == 0 {
			continue
		}
		candidate.Source = Source{Kind: "login"}
		if verify(candidate) {
			return candidate, nil
		}
	}
	return Candidate{}, apperr.New("not_authenticated", "login was not completed in time")
}

func chromeBinary() string {
	for _, path := range []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/usr/bin/google-chrome", "/usr/bin/chromium"} {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}
