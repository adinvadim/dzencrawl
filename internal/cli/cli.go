// Package cli implements the dzen command line.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/adinvadim/dzencrawl/internal/apperr"
	"github.com/adinvadim/dzencrawl/internal/config"
	"github.com/adinvadim/dzencrawl/internal/session"
	"github.com/adinvadim/dzencrawl/internal/store"
)

// App holds the process-level dependencies.
type App struct {
	Version string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	// BaseURL overrides dzen.ru in tests.
	BaseURL string
	// IsTTY overrides terminal detection in tests.
	IsTTY func() bool
	// Importer overrides browser access in tests.
	Importer func(ctx context.Context, opts session.ImportOptions, previous session.Source) ([]session.Candidate, error)
	Sleep    func(ctx context.Context, d time.Duration) error
	Now      func() time.Time
}

var valueFlags = map[string]bool{"profile": true, "config": true, "dir": true, "at": true, "wait": true, "limit": true, "cursor": true, "state": true, "format": true, "to": true, "article": true, "text": true, "sort": true, "reaction": true, "reason": true, "browser": true, "browser-profile": true, "max-articles": true, "db": true}

var boolFlags = map[string]bool{"help": true, "version": true, "json": true, "plain": true, "no-input": true, "dry-run": true, "yes": true, "force": true, "no-captcha": true, "quiet": true, "verbose": true, "no-color": true, "all": true, "remote": true, "live": true, "stdin": true, "channel": true, "delete-all": true, "import-remote": true, "mine": true, "refresh": true}

var shortFlags = map[string]string{"h": "help", "q": "quiet", "v": "verbose"}

var globalFlags = []string{"help", "version", "profile", "config", "json", "plain", "no-input", "no-captcha", "quiet", "verbose", "no-color"}

type parsed struct {
	values map[string]string
	bools  map[string]bool
	rest   []string
}

func parseArgs(args []string) (parsed, error) {
	out := parsed{values: map[string]string{}, bools: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out.rest = append(out.rest, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			out.rest = append(out.rest, arg)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if long, ok := shortFlags[name]; ok && !strings.HasPrefix(arg, "--") {
			name = long
		}
		switch {
		case boolFlags[name]:
			if hasValue {
				return out, apperr.New("usage_error", "--%s does not take a value", name)
			}
			out.bools[name] = true
		case valueFlags[name]:
			if !hasValue {
				if i+1 >= len(args) {
					return out, apperr.New("usage_error", "--%s needs a value", name)
				}
				i++
				value = args[i]
			}
			out.values[name] = value
		default:
			return out, apperr.New("usage_error", "unknown flag %s", arg)
		}
	}
	return out, nil
}

type command struct {
	path    string
	args    string
	summary string
	flags   []string
	run     func(*runContext) error
}

// Main runs the CLI and returns the process exit code.
func Main(ctx context.Context, app App, args []string) int {
	if app.Now == nil {
		app.Now = time.Now
	}
	if app.Sleep == nil {
		app.Sleep = sleepContext
	}
	if app.IsTTY == nil {
		app.IsTTY = func() bool {
			info, err := os.Stdin.Stat()
			return err == nil && info.Mode()&os.ModeCharDevice != 0
		}
	}
	run := &runContext{app: app, ctx: ctx}
	err := run.execute(args)
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) {
		err = apperr.New("interrupted", "cancelled")
	}
	return run.fail(err)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

type runContext struct {
	app     App
	ctx     context.Context
	cfg     config.Config
	flags   parsed
	args    []string
	command string
	journal string
	profile string
	st      *store.Store
}

func (c *runContext) has(name string) bool     { return c.flags.bools[name] }
func (c *runContext) value(name string) string { return c.flags.values[name] }
func (c *runContext) jsonMode() bool           { return c.has("json") }

func (c *runContext) execute(args []string) error {
	flags, err := parseArgs(args)
	if err != nil {
		return err
	}
	c.flags = flags
	if flags.bools["version"] {
		fmt.Fprintln(c.app.Stdout, c.app.Version)
		return nil
	}
	cmd, rest := lookup(flags.rest)
	if len(flags.rest) > 0 && flags.rest[0] == "help" {
		cmd, _ = lookup(flags.rest[1:])
		c.printHelp(cmd)
		return nil
	}
	if flags.bools["help"] || cmd == nil {
		c.printHelp(cmd)
		if cmd == nil && len(flags.rest) > 0 && !flags.bools["help"] {
			return apperr.New("usage_error", "unknown command %q", strings.Join(flags.rest, " "))
		}
		return nil
	}
	allowed := map[string]bool{}
	for _, f := range append(append([]string{}, globalFlags...), cmd.flags...) {
		allowed[f] = true
	}
	for name := range flags.bools {
		if !allowed[name] {
			return apperr.New("usage_error", "%s does not accept --%s", cmd.path, name)
		}
	}
	for name := range flags.values {
		if !allowed[name] {
			return apperr.New("usage_error", "%s does not accept --%s", cmd.path, name)
		}
	}
	c.command, c.args = cmd.path, rest
	if strings.TrimSpace(os.Getenv("DZEN_HOME")) == "" {
		if moved, err := config.MigrateLegacyHome("~/.dzencrawl", config.DefaultBase); err != nil {
			fmt.Fprintln(c.app.Stderr, "dzen: warning:", err)
		} else if moved {
			fmt.Fprintln(c.app.Stderr, "dzen: moved ~/.dzencrawl to ~/.dzen")
		}
	}
	if c.cfg, err = config.Load(c.value("config")); err != nil {
		return apperr.New("usage_error", "config: %v", err)
	}
	return cmd.run(c)
}

func (c *runContext) need(count int, usage string) error {
	if len(c.args) != count {
		return apperr.New("usage_error", "usage: dzen %s", usage)
	}
	return nil
}

func (c *runContext) store() (*store.Store, error) {
	if c.st != nil {
		return c.st, nil
	}
	if err := os.MkdirAll(dirOf(c.cfg.DBPath), 0o700); err != nil {
		return nil, err
	}
	st, err := store.Open(c.cfg.DBPath)
	if err != nil {
		return nil, err
	}
	c.st = st
	return st, nil
}

func dirOf(path string) string {
	if i := strings.LastIndexByte(path, '/'); i > 0 {
		return path[:i]
	}
	return "."
}

// emit writes a successful result as one JSON document or as human text.
func (c *runContext) emit(outcome string, data any, human func(io.Writer)) error {
	if c.jsonMode() {
		meta := map[string]any{"as_of": c.app.Now().UTC().Format(time.RFC3339)}
		if c.profile != "" {
			meta["profile"] = c.profile
		}
		if c.journal != "" {
			meta["journal_id"] = c.journal
		}
		return writeJSON(c.app.Stdout, map[string]any{"ok": true, "command": c.command, "outcome": outcome, "data": data, "meta": meta})
	}
	if human != nil && !c.has("quiet") {
		human(c.app.Stdout)
	}
	return nil
}

// fail reports an error and returns its exit code.
func (c *runContext) fail(err error) int {
	e := apperr.As(err)
	outcome := "rejected"
	if e.Code == "outcome_unknown" {
		outcome = "outcome_unknown"
	}
	if c.jsonMode() {
		meta := map[string]any{}
		if c.journal != "" {
			meta["journal_id"] = c.journal
		}
		_ = writeJSON(c.app.Stdout, map[string]any{"ok": false, "command": c.command, "outcome": outcome, "error": e, "meta": meta})
	}
	fmt.Fprintln(c.app.Stderr, "dzen:", e.Error())
	if e.ReconcileHint != "" {
		fmt.Fprintln(c.app.Stderr, "dzen:", e.ReconcileHint)
	}
	return apperr.Exit(e.Code)
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// confirm reports whether the user allows an action. --yes always does; a
// prompt appears only on a terminal, and --no-input forbids it.
func (c *runContext) confirm(prompt string) bool {
	if c.has("yes") {
		return true
	}
	if c.has("no-input") || !c.app.IsTTY() {
		return false
	}
	fmt.Fprintf(c.app.Stderr, "%s [y/N] ", prompt)
	line, _ := bufio.NewReader(c.app.Stdin).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

func (c *runContext) limit(fallback int) (int, error) {
	raw := c.value("limit")
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, apperr.New("usage_error", "--limit must be a positive number")
	}
	return n, nil
}

func lookup(positional []string) (*command, []string) {
	var best *command
	bestLen := 0
	for i := range commands {
		parts := strings.Fields(commands[i].path)
		if len(parts) > len(positional) || len(parts) <= bestLen {
			continue
		}
		match := true
		for j, part := range parts {
			if positional[j] != part {
				match = false
				break
			}
		}
		if match {
			best, bestLen = &commands[i], len(parts)
		}
	}
	if best == nil {
		return nil, nil
	}
	return best, positional[bestLen:]
}

func (c *runContext) printHelp(cmd *command) {
	w := c.app.Stdout
	if cmd != nil {
		fmt.Fprintf(w, "dzen %s %s\n\n%s\n", cmd.path, cmd.args, cmd.summary)
		if len(cmd.flags) > 0 {
			flags := append([]string{}, cmd.flags...)
			sort.Strings(flags)
			fmt.Fprintf(w, "\nFlags: --%s\n", strings.Join(flags, " --"))
		}
		fmt.Fprintf(w, "\nGlobal flags: --%s\n", strings.Join(globalFlags, " --"))
		return
	}
	fmt.Fprintln(w, "dzen — manage Dzen articles and comments; an article is a Markdown file\n\nUsage: dzen [global flags] <command> [args]\n\nCommands:")
	for _, item := range commands {
		fmt.Fprintf(w, "  %-22s %s\n", item.path, item.summary)
	}
	fmt.Fprintf(w, "\nGlobal flags: --%s\nRun dzen <command> --help for details.\n", strings.Join(globalFlags, " --"))
}
