package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"repairflow/internal/app"
	"repairflow/internal/db"
	"repairflow/internal/logging"
)

func failedService(t *testing.T) *app.Service {
	t.Helper()
	handle, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	svc := app.New(handle)
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestWebReadFailuresAreNotIgnored(t *testing.T) {
	var logs bytes.Buffer
	srv, err := New(failedService(t))
	if err != nil {
		t.Fatal(err)
	}
	srv.WithLogger(logging.New(false, &logs))
	req := httptest.NewRequest(http.MethodGet, "http://web.test/", nil)
	rec := httptest.NewRecorder()
	srv.dashboard(rec, req, app.Session{Username: "test", Role: app.RoleAdmin, CSRFToken: "test"})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("dashboard database failure must produce 500, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "internal application error") || strings.Contains(rec.Body.String(), "sql: database is closed") {
		t.Fatalf("internal response must be safe and generic: %q", rec.Body.String())
	}
	if !strings.Contains(logs.String(), "operation=request") || !strings.Contains(logs.String(), "database is closed") {
		t.Fatalf("server diagnostics must retain the internal cause: %q", logs.String())
	}
	if _, err := srv.repairViewData(context.Background(), 1); err == nil {
		t.Fatal("repair view must propagate database failure")
	}
}

func TestLogoutFailureDoesNotClaimSuccess(t *testing.T) {
	srv, err := New(failedService(t))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://web.test/logout", nil)
	req.AddCookie(&http.Cookie{Name: "repairflow_session", Value: "token"})
	rec := httptest.NewRecorder()
	srv.logout(rec, req, app.Session{Username: "test", Role: app.RoleAdmin})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("failed session revocation must produce 500, got %d", rec.Code)
	}
}
