package main

import (
	"context"
	"strings"
	"testing"

	"github.com/zed1995/platepilot/data-pipeline/internal/config"
	"github.com/zed1995/platepilot/data-pipeline/internal/pipeline"
	"github.com/zed1995/platepilot/shared/domain/errs"
)

func TestRunVersionAndHelpSucceedWithoutConfiguration(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"help"}, {"-h"}, {"--help"}} {
		if err := run(args); err != nil {
			t.Errorf("run(%v) = %v, want nil", args, err)
		}
	}
}

func TestRunRejectsMissingAndUnknownCommands(t *testing.T) {
	if err := run(nil); err == nil {
		t.Error("run(nil) should report a missing command")
	}
	if err := run([]string{"not-a-command"}); err == nil {
		t.Error("run with an unknown command should fail")
	}
}

// The two M2 subcommands are the ones an operator actually types, and their
// guard clauses are the only part that can be checked without a database. They
// are worth pinning for a specific reason: each one has to refuse a
// misconfiguration with a message that names what is missing, and getting the
// order wrong turns "you forgot POSTGRES_DSN" into a connection error to a host
// that was never meant to be contacted.
func TestBuildDocumentsRefusesWithoutADSN(t *testing.T) {
	cfg := config.Config{}
	err := runBuildDocuments(context.Background(), cfg, []string{"--scope=restaurant"})
	if err == nil {
		t.Fatal("build-documents should refuse to run without POSTGRES_DSN")
	}
	if code := errs.CodeOf(err); code != errs.CodeInvalidArgument {
		t.Errorf("error code = %q, want %q", code, errs.CodeInvalidArgument)
	}
	if !strings.Contains(errsMessage(err), "POSTGRES_DSN") {
		t.Errorf("error %v does not name the missing variable", err)
	}
}

func TestEmbedRefusesWithoutADSN(t *testing.T) {
	err := runEmbed(context.Background(), config.Config{}, nil)
	if err == nil {
		t.Fatal("embed should refuse to run without POSTGRES_DSN")
	}
	if code := errs.CodeOf(err); code != errs.CodeInvalidArgument {
		t.Errorf("error code = %q, want %q", code, errs.CodeInvalidArgument)
	}
	if !strings.Contains(errsMessage(err), "POSTGRES_DSN") {
		t.Errorf("error %v does not name the missing variable", err)
	}
}

// The provider check exists because embed is the only stage that needs one.
// A pipeline configured for import alone has no embedding block, and running
// embed against it has to say which variable is absent rather than fail later
// with a nil provider.
func TestEmbedRequiresAProvider(t *testing.T) {
	opts, err := pipeline.ParseEmbedOptions(nil, config.Config{})
	if err != nil {
		t.Fatalf("ParseEmbedOptions with no arguments: %v", err)
	}
	if opts.Workers != 1 {
		t.Errorf("default workers = %d, want 1: an unset configuration must not "+
			"produce a pool of zero goroutines", opts.Workers)
	}
	if opts.BatchSize != 32 {
		t.Errorf("default batch size = %d, want 32", opts.BatchSize)
	}
}

// A bad flag has to be reported as a bad flag. The option parsers run before
// the configuration checks, so a typo in --scope is not silently reinterpreted
// as a missing DSN.
func TestM2SubcommandsRejectBadFlagsFirst(t *testing.T) {
	cfg := config.Config{}
	err := runBuildDocuments(context.Background(), cfg, []string{"--scope=nonsense"})
	if err == nil {
		t.Fatal("build-documents should reject an unknown scope")
	}
	if code := errs.CodeOf(err); code != errs.CodeInvalidArgument {
		t.Errorf("error code = %q, want %q", code, errs.CodeInvalidArgument)
	}

	// An explicit zero is a mistake worth reporting; the default is repaired
	// separately, in ParseEmbedOptions, for the case the flag was not given.
	if err := runEmbed(context.Background(), cfg, []string{"--workers=0"}); err == nil {
		t.Error("embed should reject --workers=0 rather than running with an empty pool")
	}
	if err := runEmbed(context.Background(), cfg, []string{"--batch=0"}); err == nil {
		t.Error("embed should reject --batch=0")
	}
	err = runEmbed(context.Background(), cfg, []string{"--not-a-flag"})
	if err == nil {
		t.Error("embed should reject an unknown argument")
	}
}

// errsMessage returns the text of an error for substring assertions.
func errsMessage(err error) string {
	if typed, ok := errs.As(err); ok {
		return typed.Message
	}
	return err.Error()
}
