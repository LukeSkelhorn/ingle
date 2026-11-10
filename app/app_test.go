package app_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/app"
)

// freeAddr returns a loopback address with a port nothing is listening on.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func TestRunServesAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INGLE_ADDR", freeAddr(t))
	t.Setenv("INGLE_DB_PATH", filepath.Join(dir, "ingle.db"))
	t.Setenv("INGLE_BACKUP_DIR", filepath.Join(dir, "backups"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx, app.Options{Version: "test"}) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		err := app.Healthcheck(context.Background())
		if err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Run returned early: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("healthz never came up: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	path, err := app.Backup(context.Background())
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("backup file: %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run after cancel: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
