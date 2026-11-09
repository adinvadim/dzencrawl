package session

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"
	_ "modernc.org/sqlite"

	"github.com/adinvadim/dzencrawl/internal/apperr"
)

func TestCompactKeepsOneValuePerNameAndPrefersDzen(t *testing.T) {
	got := Compact([]Cookie{
		{Name: "Session_id", Value: "yandex", Domain: ".yandex.ru"},
		{Name: "Session_id", Value: "dzen", Domain: ".dzen.ru"},
		{Name: "i", Value: "1", Domain: "sso.passport.yandex.ru"},
		{Name: "other", Value: "x", Domain: ".example.com"},
		{Name: "empty", Value: "", Domain: ".dzen.ru"},
	})
	if Header(got) != "Session_id=dzen; i=1" {
		t.Fatal(Header(got))
	}
}

func TestStoreSavesPrivatelyAndSelectsSoleProfile(t *testing.T) {
	st := Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if _, err := st.Current(); !apperr.Is(err, "not_authenticated") {
		t.Fatal(err)
	}
	if err := st.Save(Session{Profile: "acc", Cookies: []Cookie{{Name: "a", Value: "b"}}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(st.Dir, "acc", "session.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", info, err)
	}
	if current, err := st.Current(); err != nil || current != "acc" {
		t.Fatal(current, err)
	}
	if err := st.Save(Session{Profile: "two"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Current(); !apperr.Is(err, "usage_error") {
		t.Fatal(err)
	}
	if err := st.Use("two"); err != nil {
		t.Fatal(err)
	}
	if current, _ := st.Current(); current != "two" {
		t.Fatal(current)
	}
	if err := st.Delete("two"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load("two"); !apperr.Is(err, "not_authenticated") {
		t.Fatal(err)
	}
}

func TestValidProfileRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"", "..", "a/b", "../x", "a..b"} {
		if ValidProfile(bad) {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func encrypt(t *testing.T, key []byte, plain string) []byte {
	t.Helper()
	data := []byte(plain)
	pad := aes.BlockSize - len(data)%aes.BlockSize
	data = append(data, bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(data))
	cipher.NewCBCEncrypter(block, bytes.Repeat([]byte(" "), aes.BlockSize)).CryptBlocks(out, data)
	return append([]byte("v10"), out...)
}

func TestReadCookieDBDecryptsModernAndOlderSchemas(t *testing.T) {
	key, err := deriveKey("secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		version int
		prefix  string
	}{{23, ""}, {24, strings.Repeat("h", 32)}} {
		dir := t.TempDir()
		db, err := sql.Open("sqlite", filepath.Join(dir, "Cookies"))
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{`create table meta(key text, value text)`, `create table cookies(host_key text,name text,encrypted_value blob,value text)`} {
			if _, err := db.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(`insert into meta values('version',?)`, tc.version); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`insert into cookies values('.dzen.ru','Session_id',?,'')`, encrypt(t, key, tc.prefix+"value-1")); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`insert into cookies values('.example.com','x',?,'')`, encrypt(t, key, tc.prefix+"nope")); err != nil {
			t.Fatal(err)
		}
		db.Close()
		got, err := readCookieDB(context.Background(), dir, key)
		if err != nil || len(got) != 1 || got[0].Value != "value-1" {
			t.Fatalf("schema %d: %+v %v", tc.version, got, err)
		}
	}
}

func TestDecryptCookieRejectsUnknownFormats(t *testing.T) {
	key, _ := deriveKey("secret")
	if _, err := decryptCookie([]byte("v20abcdefghijklmnop"), key, "h", 24); err == nil {
		t.Fatal("v20 accepted")
	}
	if _, err := decryptCookie(append([]byte("v10"), 1, 2, 3), key, "h", 24); err == nil {
		t.Fatal("short block accepted")
	}
}

func TestFromCDPReadsCookiesAndUserAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			_, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			var req struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			_ = json.Unmarshal(data, &req)
			result := map[string]any{"userAgent": "UA/1"}
			if req.Method == "Storage.getCookies" {
				result = map[string]any{"cookies": []Cookie{{Name: "Session_id", Value: "v", Domain: ".dzen.ru"}}}
			}
			payload, _ := json.Marshal(map[string]any{"id": req.ID, "result": result})
			_ = c.Write(r.Context(), websocket.MessageText, payload)
		}
	}))
	defer srv.Close()
	root := t.TempDir()
	host := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
	if err := os.WriteFile(filepath.Join(root, "DevToolsActivePort"), []byte(host+"\n/devtools/browser/x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := FromCDP(context.Background(), root)
	if err != nil || got.UserAgent != "UA/1" || got.Cookies[0].Value != "v" {
		t.Fatalf("%+v %v", got, err)
	}
}
