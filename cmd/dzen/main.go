// Command dzen manages Dzen articles and comments from the terminal.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/adinvadim/dzencrawl/internal/cli"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Main(ctx, cli.App{Version: version, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}, os.Args[1:])
	stop()
	os.Exit(code)
}
