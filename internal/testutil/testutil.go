// Package testutil holds the failure-fast helpers shared by test fixtures.
package testutil

import (
	"io"
	"testing"
)

// Must fails the test when a fixture step returns an unexpected error.
func Must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Close closes c and fails the test on error, so a fixture's deferred close
// does not silently drop its error.
func Close(t *testing.T, c io.Closer) {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Errorf("close %T: %v", c, err)
	}
}
