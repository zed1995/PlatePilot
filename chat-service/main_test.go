package main

import (
	"strings"
	"testing"
)

// TestResolveCommandDefaultsToServe pins the compatibility rule: adding
// subcommands must not turn an existing deployment's command line into an error,
// so an empty argument list still means "serve".
func TestResolveCommandDefaultsToServe(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "no arguments", args: nil, want: commandServe},
		{name: "explicit serve", args: []string{"serve"}, want: commandServe},
		{name: "run alias", args: []string{"run"}, want: commandServe},
		{name: "check-config", args: []string{"check-config"}, want: commandCheckConfig},
		{name: "version", args: []string{"version"}, want: commandVersion},
		{name: "help flag", args: []string{"--help"}, want: commandHelp},
		{name: "help word", args: []string{"help"}, want: commandHelp},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveCommand(tc.args)
			if err != nil {
				t.Fatalf("resolveCommand(%v): %v", tc.args, err)
			}
			if got != tc.want {
				t.Fatalf("resolveCommand(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

func TestResolveCommandReportsAnUnknownCommand(t *testing.T) {
	_, err := resolveCommand([]string{"check-connection"})
	if err == nil {
		t.Fatal("want an error for an unknown command")
	}
	if !strings.Contains(err.Error(), "check-connection") {
		t.Fatalf("the error should name the command that was rejected: %v", err)
	}
	if !strings.Contains(err.Error(), "check-config") {
		t.Fatalf("the error should show the usage so the typo is visible: %v", err)
	}
}

// TestCheckConfigRejectsAnInvalidEnvironment covers the failure path: the
// command must report a bad configuration rather than printing a summary of one.
func TestCheckConfigRejectsAnInvalidEnvironment(t *testing.T) {
	t.Setenv("HTTP_ADDR", "::::")
	if err := checkConfig(); err == nil {
		t.Fatal("want an error for an invalid HTTP_ADDR")
	}
	t.Setenv("HTTP_ADDR", ":8080")
	t.Setenv("LOG_LEVEL", "loud")
	if err := checkConfig(); err == nil {
		t.Fatal("want an error for an invalid LOG_LEVEL")
	}
}
