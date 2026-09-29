package mongo_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tryvium-travels/memongo"
	"github.com/tryvium-travels/memongo/memongolog"
)

// TestMain injects a MongoDB instance for the package's integration tests.
//
// Resolution order:
//  1. MONGO_URI is set            -> use that cluster (real Atlas or CI service)
//  2. PLATEPILOT_MEMONGO is truthy -> download/start a real mongod in a temp dir
//     and point MONGO_URI at it
//  3. otherwise                    -> stay offline; the Atlas tests skip
//
// Option 2 is opt-in so `go test ./...` stays offline and fast by default, but
// the *same* contract suite covers both the in-memory store and a real server.
func TestMain(m *testing.M) {
	stop, err := startMemongoIfRequested()
	if err != nil {
		fmt.Fprintf(os.Stderr, "platepilot: failed to start memongo: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	stop()
	os.Exit(code)
}

// defaultMemongoVersion must be >= 7.0 on darwin/arm64: MongoDB 6.x standalone
// uses the ephemeralForTest storage engine, which 6.0 removed, while 7.x and
// later default to wiredTiger. It also satisfies memongo's arm64 requirement of
// MongoDB >= 6.0.
const defaultMemongoVersion = "7.0.14"

func startMemongoIfRequested() (func(), error) {
	if os.Getenv("MONGO_URI") != "" {
		return func() {}, nil
	}
	if !truthy(os.Getenv("PLATEPILOT_MEMONGO")) {
		return func() {}, nil
	}

	version := os.Getenv("PLATEPILOT_MEMONGO_VERSION")
	if version == "" {
		version = defaultMemongoVersion
	}
	cache := os.Getenv("MEMONGO_CACHE_PATH")
	if cache == "" {
		// Keep the downloaded binary inside the writable temp dir instead of
		// ~/Library/Caches so sandboxed runs work.
		cache = filepath.Join(os.TempDir(), "platepilot-memongo")
	}

	server, err := memongo.StartWithOptions(&memongo.Options{
		MongoVersion: version,
		CachePath:    cache,
		LogLevel:     memongolog.LogLevelWarn,
	})
	if err != nil {
		return nil, err
	}
	if err := os.Setenv("MONGO_URI", server.URI()); err != nil {
		server.Stop()
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "platepilot: memongo %s listening on %s (cache %s)\n", version, server.URI(), cache)
	return server.Stop, nil
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
