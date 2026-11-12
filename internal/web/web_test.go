package web

import (
	"io/fs"
	"testing"
)

func TestEmbeddedAssets(t *testing.T) {
	assets := Assets()
	body, err := fs.ReadFile(assets, "app.css")
	if err != nil {
		t.Fatalf("embedded asset missing: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("embedded asset is empty")
	}
}
