package main

import "testing"

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
