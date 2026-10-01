package server

import (
	"io"
	"log/slog"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Failed sign-ins and network changes log on purpose; keep test output clean.
	if os.Getenv("PBCM_TEST_LOG") == "" {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	os.Exit(m.Run())
}
