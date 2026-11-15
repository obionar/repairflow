package backup

import (
	"bytes"
	"strings"
	"testing"
)

// A long-lived workshop inventory can exceed the old 4 MiB manifest reader
// bound while every attachment remains valid. Writer and reader must agree on
// the same limit.
func TestLargeManifestRoundtrips(t *testing.T) {
	hash := strings.Repeat("a", 64)
	m := Manifest{
		FormatVersion: FormatVersion,
		SchemaVersion: 9,
		Database:      FileEntry{Name: dbEntryName, SizeBytes: 1, SHA256: hash},
		Attachments:   make([]AttachmentEntry, 22000),
	}
	for i := range m.Attachments {
		m.Attachments[i] = AttachmentEntry{TicketID: int64(i + 1), StorageName: strings.Repeat("b", 32), SizeBytes: 1, SHA256: hash}
	}
	body, err := encodeManifest(&m)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(body) <= 4<<20 {
		t.Fatalf("regression fixture is not larger than the old limit: %d bytes", len(body))
	}
	got, err := decodeManifest(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("decode %d-byte manifest: %v", len(body), err)
	}
	if len(got.Attachments) != len(m.Attachments) {
		t.Fatalf("attachments: got %d want %d", len(got.Attachments), len(m.Attachments))
	}
}

func TestManifestRejectsUnsafeAttachmentPaths(t *testing.T) {
	for _, tc := range []AttachmentEntry{
		{TicketID: 0, StorageName: strings.Repeat("b", 32)},
		{TicketID: -1, StorageName: strings.Repeat("b", 32)},
		{TicketID: 1, StorageName: "../../../outside"},
		{TicketID: 1, StorageName: "nested/" + strings.Repeat("b", 32)},
		{TicketID: 1, StorageName: strings.Repeat("z", 32)},
	} {
		tc.SizeBytes = 1
		tc.SHA256 = strings.Repeat("a", 64)
		m := Manifest{FormatVersion: FormatVersion, SchemaVersion: 10,
			Database:    FileEntry{Name: dbEntryName, SizeBytes: 1, SHA256: tc.SHA256},
			Attachments: []AttachmentEntry{tc}}
		if err := checkManifest(&m); err == nil {
			t.Errorf("unsafe attachment path accepted: %+v", tc)
		}
	}
}

func TestOversizedManifestIsRefused(t *testing.T) {
	body := bytes.Repeat([]byte{'x'}, MaxManifestBytes+1)
	if _, err := decodeManifest(bytes.NewReader(body)); err == nil {
		t.Fatal("oversized manifest must be refused")
	}
}
