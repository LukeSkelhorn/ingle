// Command ingle runs the Ingle family chores board server.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/lukeskelhorn/ingle/app"
)

var version = "dev"

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	var err error
	switch cmd {
	case "":
		err = app.Run(ctx, app.Options{Version: version, Log: log})
	case "healthcheck":
		// Docker's HEALTHCHECK: the image has no shell or curl.
		err = app.Healthcheck(ctx)
	case "backup":
		var path string
		if path, err = app.Backup(ctx); err == nil {
			fmt.Println(path)
		}
	case "version", "--version", "-v":
		fmt.Println("ingle", version)
	default:
		stop()
		fmt.Fprintf(os.Stderr, "usage: ingle [healthcheck|backup|version]\n")
		os.Exit(2)
	}
	stop()
	if err != nil {
		log.Error("ingle "+cmd+" failed", slog.Any("err", err))
		os.Exit(1)
	}
}
