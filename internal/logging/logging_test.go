package logging

import (
	"bytes"
	"strings"
	"testing"
)

func TestDebugEnabled(t *testing.T) {
	for _, value := range []string{"", "0", "false", "FALSE", " 0 "} {
		if DebugEnabled(false, value) {
			t.Errorf("environment %q must leave debug disabled", value)
		}
	}
	for _, value := range []string{"1", "true", "yes"} {
		if !DebugEnabled(false, value) {
			t.Errorf("environment %q must enable debug", value)
		}
	}
	if !DebugEnabled(true, "0") {
		t.Fatal("explicit --debug must enable debug")
	}
}

func TestDebugControlsLogVerbosity(t *testing.T) {
	var output bytes.Buffer
	New(false, &output).Debug("hidden", "operation", "test")
	if output.Len() != 0 {
		t.Fatalf("normal logger emitted debug record: %q", output.String())
	}
	New(true, &output).Debug("visible", "component", "test", "operation", "test")
	for _, want := range []string{"level=DEBUG", "component=test", "operation=test"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("debug record missing %q: %q", want, output.String())
		}
	}
}
