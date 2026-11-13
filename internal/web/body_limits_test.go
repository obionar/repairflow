package web

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"repairflow/internal/app"
	"repairflow/internal/db"
	"repairflow/internal/files"
	"repairflow/internal/testutil"
)

type countingReader struct {
	io.Reader
	n int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.n += int64(n)
	return n, err
}

func newBodyLimitServer(t *testing.T) (*Server, *app.Service) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "ws")
	handle, err := db.Open(context.Background(), db.Path(home))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if err := db.Migrate(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	svc := app.New(handle).WithStore(files.New(home))
	for _, user := range []struct {
		name, role string
	}{{"admin", app.RoleAdmin}, {"tech", app.RoleTechnician}} {
		if _, err := svc.CreateUser(context.Background(), "setup", user.name, "password12345", user.role); err != nil {
			t.Fatal(err)
		}
	}
	srv, err := New(svc)
	if err != nil {
		t.Fatal(err)
	}
	return srv, svc
}

func multipartBody(t *testing.T, size int, fields map[string]string) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "large.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(file, bytes.NewReader(bytes.Repeat([]byte("x"), size)), int64(size)); err != nil {
		t.Fatal(err)
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), writer.FormDataContentType()
}

func TestLoginMultipartBodyCapRejectsBeforeFullRead(t *testing.T) {
	srv, _ := newBodyLimitServer(t)
	srv.loginBodyBytes = 1024
	for _, chunked := range []bool{false, true} {
		t.Run(strconv.FormatBool(chunked), func(t *testing.T) {
			body, contentType := multipartBody(t, 2<<20, map[string]string{
				"username": "admin", "password": "wrong-password",
			})
			reader := &countingReader{Reader: bytes.NewReader(body)}
			req := httptest.NewRequest(http.MethodPost, "http://web.test/login", reader)
			req.Header.Set("Content-Type", contentType)
			req.Header.Set("Origin", "http://web.test")
			if chunked {
				req.ContentLength = -1
			}
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("oversized login status = %d, want 413", rec.Code)
			}
			if reader.n >= int64(len(body)) {
				t.Fatalf("complete unauthenticated multipart body was consumed: %d/%d", reader.n, len(body))
			}
		})
	}
}

func loginForBodyLimit(t *testing.T, srv *Server, username string) (*http.Cookie, string) {
	t.Helper()
	form := url.Values{"username": {username}, "password": {"password12345"}}
	req := httptest.NewRequest(http.MethodPost, "http://web.test/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://web.test")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	var cookie *http.Cookie
	for _, candidate := range rec.Result().Cookies() {
		if candidate.Name == "repairflow_session" {
			cookie = candidate
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie")
	}
	req = httptest.NewRequest(http.MethodGet, "http://web.test/settings", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	marker := `name="csrf_token" value="`
	index := strings.Index(body, marker)
	if index < 0 {
		t.Fatal("CSRF token not found")
	}
	rest := body[index+len(marker):]
	return cookie, rest[:strings.Index(rest, `"`)]
}

func TestAuthenticatedOversizedFormIsRejected(t *testing.T) {
	srv, _ := newBodyLimitServer(t)
	srv.formBodyBytes = 1024
	cookie, csrf := loginForBodyLimit(t, srv, "tech")
	form := url.Values{"csrf_token": {csrf}, "language": {"el"}, "padding": {strings.Repeat("x", 4096)}}
	req := httptest.NewRequest(http.MethodPost, "http://web.test/settings/language", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://web.test")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("authenticated oversized form status = %d, want 413", rec.Code)
	}
}

func TestAttachmentRouteRetainsLargeAllowance(t *testing.T) {
	srv, svc := newBodyLimitServer(t)
	srv.formBodyBytes = 1024
	srv.attachmentBodyBytes = 8192
	srv.WithAuthentication(false)
	repair, err := svc.CreateRepair(context.Background(), "setup", app.RepairInput{Device: "Upload target", Issue: "Issue"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/repairs/" + strconv.FormatInt(repair.ID, 10) + "/attachments"
	for _, tc := range []struct {
		name string
		size int
		want int
	}{{"allowed", 2048, http.StatusSeeOther}, {"too-large", 16384, http.StatusRequestEntityTooLarge}} {
		t.Run(tc.name, func(t *testing.T) {
			body, contentType := multipartBody(t, tc.size, map[string]string{"caption": "upload"})
			req := httptest.NewRequest(http.MethodPost, "http://web.test"+path, bytes.NewReader(body))
			req.Header.Set("Content-Type", contentType)
			req.Header.Set("Origin", "http://web.test")
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("attachment upload status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestSlowLoginBodyUsesReadDeadline(t *testing.T) {
	srv, _ := newBodyLimitServer(t)
	srv.loginReadTimeout = 100 * time.Millisecond
	server := httptest.NewServer(srv.Handler())
	defer server.Close()

	reader, writer := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/login", reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", server.URL)
	started := time.Now()
	done := make(chan struct {
		resp *http.Response
		err  error
	}, 1)
	go func() {
		resp, err := server.Client().Do(req)
		done <- struct {
			resp *http.Response
			err  error
		}{resp, err}
	}()
	if _, err := io.WriteString(writer, "username=admin&password=wrong-password"); err != nil {
		t.Fatal(err)
	}
	// Keep the body open and incomplete. A working server-side deadline must
	// finish the request without waiting for the slow writer to close it.
	var result struct {
		resp *http.Response
		err  error
	}
	select {
	case result = <-done:
	case <-time.After(900 * time.Millisecond):
		t.Fatal("slow body held the connection beyond the configured read deadline")
	}
	_ = writer.Close()
	elapsed := time.Since(started)
	if result.err == nil {
		defer testutil.Close(t, result.resp.Body)
		if result.resp.StatusCode >= 200 && result.resp.StatusCode < 400 {
			t.Fatalf("slow body unexpectedly succeeded: %s", result.resp.Status)
		}
	}
	if elapsed > 900*time.Millisecond {
		t.Fatalf("slow body held the connection for %v; read deadline was not applied", elapsed)
	}
}
