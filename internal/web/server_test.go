package web_test

import (
	"bytes"
	"context"
	"log/slog"
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
	"repairflow/internal/logging"
	"repairflow/internal/web"
)

type webFixture struct {
	t       *testing.T
	svc     *app.Service
	handler http.Handler
}

func newWeb(t *testing.T) *webFixture {
	t.Helper()
	return newWebWithLogger(t, nil)
}

func newWebWithLogger(t *testing.T, logger *slog.Logger) *webFixture {
	t.Helper()
	home := filepath.Join(t.TempDir(), "ws")
	handle, err := db.Open(context.Background(), db.Path(home))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { handle.Close() })
	if err := db.Migrate(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	svc := app.New(handle).WithStore(files.New(home))
	for _, u := range []struct{ name, role string }{
		{"admin", app.RoleAdmin}, {"tech", app.RoleTechnician}, {"viewer", app.RoleViewer},
	} {
		if _, err := svc.CreateUser(context.Background(), "setup", u.name, "password12345", u.role); err != nil {
			t.Fatal(err)
		}
	}
	srv, err := web.New(svc)
	if err != nil {
		t.Fatal(err)
	}
	srv.WithLogger(logger)
	return &webFixture{t: t, svc: svc, handler: srv.Handler()}
}

func (f *webFixture) login(username, password string) *http.Cookie {
	f.t.Helper()
	form := url.Values{"username": {username}, "password": {password}}
	req := httptest.NewRequest("POST", "http://web.test/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://web.test")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		f.t.Fatalf("login: status %d body %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "repairflow_session" && c.Value != "" {
			return c
		}
	}
	f.t.Fatal("no session cookie issued")
	return nil
}

// TestLanguageSelectionPersistsAndRefusesUnknown checks browser language
// override: the settings form lists the shipped catalogs, a known language is
// stored in a cookie and changes the rendered page, and an unknown code is
// refused without changing the cookie.
func TestLanguageSelectionPersistsAndRefusesUnknown(t *testing.T) {
	f := newWeb(t)
	cookie := f.login("admin", "password12345")

	body := f.get("/settings", cookie)
	for _, want := range []string{`id="language"`, `value="en"`, `value="el"`, `value="ru"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("settings page missing %q", want)
		}
	}
	csrf := f.csrfFor(cookie)

	rec := f.post("/settings/language", cookie, url.Values{
		"csrf_token": {csrf}, "language": {"ru"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("language post: status %d body %s", rec.Code, rec.Body.String())
	}
	var chosen string
	for _, c := range rec.Result().Cookies() {
		if c.Name == "repairflow_lang" {
			chosen = c.Value
		}
	}
	if chosen != "ru" {
		t.Fatalf("language cookie = %q, want ru", chosen)
	}

	req := httptest.NewRequest("GET", "http://web.test/", nil)
	req.AddCookie(cookie)
	req.AddCookie(&http.Cookie{Name: "repairflow_lang", Value: "ru"})
	rec2 := httptest.NewRecorder()
	f.handler.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("dashboard with ru: status %d", rec2.Code)
	}
	if got := rec2.Body.String(); !strings.Contains(got, `<html lang="ru"`) || !strings.Contains(got, "Текущая работа") {
		t.Fatalf("dashboard not rendered in Russian")
	}

	rec3 := f.post("/settings/language", cookie, url.Values{
		"csrf_token": {csrf}, "language": {"xx"},
	})
	if rec3.Code != http.StatusBadRequest {
		t.Fatalf("unknown language: status %d, want 400", rec3.Code)
	}
	for _, c := range rec3.Result().Cookies() {
		if c.Name == "repairflow_lang" && c.Value == "xx" {
			t.Fatal("unknown language must not be stored")
		}
	}
}

// csrfFor extracts the CSRF token from the settings page (every page embeds it).
func (f *webFixture) csrfFor(cookie *http.Cookie) string {
	f.t.Helper()
	body := f.get("/settings", cookie)
	idx := strings.Index(body, `name="csrf_token" value="`)
	if idx < 0 {
		f.t.Fatal("csrf token not found in page")
	}
	rest := body[idx+len(`name="csrf_token" value="`):]
	return rest[:strings.Index(rest, `"`)]
}

func (f *webFixture) get(path string, cookie *http.Cookie) string {
	f.t.Helper()
	req := httptest.NewRequest("GET", "http://web.test"+path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		f.t.Fatalf("GET %s: status %d body %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func (f *webFixture) post(path string, cookie *http.Cookie, form url.Values) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest("POST", "http://web.test"+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://web.test")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

// ---- authentication and security ----------------------------------------

func TestLoginRequired(t *testing.T) {
	f := newWeb(t)
	req := httptest.NewRequest("GET", "http://web.test/", nil)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("unauthenticated access must redirect to login: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

func TestLoginBadPassword(t *testing.T) {
	f := newWeb(t)
	form := url.Values{"username": {"admin"}, "password": {"wrong-password"}}
	req := httptest.NewRequest("POST", "http://web.test/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://web.test")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad password: %d", rec.Code)
	}
}

func TestCSRFAndOriginProtection(t *testing.T) {
	f := newWeb(t)
	cookie := f.login("tech", "password12345")
	csrf := f.csrfFor(cookie)

	// Missing CSRF token is refused.
	rec := f.post("/customers/new", cookie, url.Values{"name": {"No CSRF"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing csrf: %d", rec.Code)
	}
	// Wrong origin is refused even with a valid token.
	req := httptest.NewRequest("POST", "http://web.test/customers/new", strings.NewReader(url.Values{
		"name": {"Bad Origin"}, "csrf_token": {csrf},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://evil.test")
	req.AddCookie(cookie)
	rec2 := httptest.NewRecorder()
	f.handler.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("cross-origin post: %d", rec2.Code)
	}
	// And nothing was created by either refusal.
	body := f.get("/customers", cookie)
	if strings.Contains(body, "No CSRF") || strings.Contains(body, "Bad Origin") {
		t.Fatal("refused requests must not create records")
	}
}

func TestRoleEnforcement(t *testing.T) {
	f := newWeb(t)

	// Viewer can read but not write.
	viewer := f.login("viewer", "password12345")
	csrf := f.csrfFor(viewer)
	rec := f.post("/customers/new", viewer, url.Values{"name": {"Viewer Attempt"}, "csrf_token": {csrf}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer write must be forbidden: %d", rec.Code)
	}
	f.get("/repairs", viewer) // reads fine

	// Technician can write but cannot purge.
	tech := f.login("tech", "password12345")
	csrf = f.csrfFor(tech)
	if rec := f.post("/customers/new", tech, url.Values{"name": {"Tech Customer"}, "csrf_token": {csrf}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("technician write: %d", rec.Code)
	}
	rec = f.post("/attachments/1/purge", tech, url.Values{"confirm": {"1"}, "csrf_token": {csrf}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("technician purge must be forbidden: %d", rec.Code)
	}
}

func TestLogoutAndSessionRevocation(t *testing.T) {
	f := newWeb(t)
	cookie := f.login("admin", "password12345")
	csrf := f.csrfFor(cookie)
	rec := f.post("/logout", cookie, url.Values{"csrf_token": {csrf}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logout: %d", rec.Code)
	}
	// Old cookie no longer authenticates.
	req := httptest.NewRequest("GET", "http://web.test/", nil)
	req.AddCookie(cookie)
	rec2 := httptest.NewRecorder()
	f.handler.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusSeeOther {
		t.Fatalf("revoked session must redirect: %d", rec2.Code)
	}
}

func TestPasswordConfirmationAndMinimumLength(t *testing.T) {
	f := newWeb(t)
	cookie := f.login("viewer", "password12345")
	csrf := f.csrfFor(cookie)

	rec := f.post("/settings/password", cookie, url.Values{
		"current_password": {"password12345"}, "new_password": {"new-password-1"},
		"confirm_password": {"different-password"}, "csrf_token": {csrf},
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "do not match") {
		t.Fatalf("mismatched confirmation must be refused: %d", rec.Code)
	}
	rec = f.post("/settings/password", cookie, url.Values{
		"current_password": {"password12345"}, "new_password": {"short"},
		"confirm_password": {"short"}, "csrf_token": {csrf},
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "at least 6") {
		t.Fatalf("short password must be refused: %d", rec.Code)
	}
	f.login("viewer", "password12345")
}

func TestPasswordChangeEndsSessions(t *testing.T) {
	f := newWeb(t)
	cookie := f.login("viewer", "password12345")
	csrf := f.csrfFor(cookie)
	rec := f.post("/settings/password", cookie, url.Values{
		"current_password": {"password12345"}, "new_password": {"new-password-1"},
		"confirm_password": {"new-password-1"}, "csrf_token": {csrf},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("password change: %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "http://web.test/", nil)
	req.AddCookie(cookie)
	rec2 := httptest.NewRecorder()
	f.handler.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusSeeOther {
		t.Fatal("password change must end existing sessions")
	}
	// New password works.
	f.login("viewer", "new-password-1")
}

func TestArchiveLifecycleInWeb(t *testing.T) {
	f := newWeb(t)
	tech := f.login("tech", "password12345")
	csrf := f.csrfFor(tech)
	ctx := context.Background()
	customer, err := f.svc.CreateCustomer(ctx, "setup", app.CustomerInput{Name: "Archive Customer"})
	if err != nil {
		t.Fatal(err)
	}
	repair, err := f.svc.CreateRepair(ctx, "setup", app.RepairInput{CustomerID: customer.ID, Device: "Archive Device", Issue: "Issue"})
	if err != nil {
		t.Fatal(err)
	}

	customerPath := "/customers/" + itoaLocal(customer.ID)
	if page := f.get(customerPath, tech); !strings.Contains(page, "customer-archive") {
		t.Fatal("active customer must offer an archive action")
	}
	if rec := f.post(customerPath+"/archive", tech, url.Values{"csrf_token": {csrf}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("archive customer: %d %s", rec.Code, rec.Body.String())
	}
	page := f.get(customerPath, tech)
	for _, want := range []string{"Archived", "customer-unarchive"} {
		if !strings.Contains(page, want) {
			t.Errorf("archived customer page missing %q", want)
		}
	}
	if list := f.get("/customers", tech); !strings.Contains(list, "Archive Customer") || !strings.Contains(list, "Archived") {
		t.Fatal("archived customer must stay listed with the archived badge")
	}
	if list := f.get("/customers", tech); strings.Contains(list, "archived-filter") {
		t.Fatal("the archived filter must not be rendered")
	}
	if rec := f.post(customerPath+"/unarchive", tech, url.Values{"csrf_token": {csrf}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("unarchive customer: %d", rec.Code)
	}
	if page := f.get(customerPath, tech); strings.Contains(page, "customer-unarchive") {
		t.Fatal("unarchived customer must return to the archive action")
	}

	repairPath := "/repairs/" + itoaLocal(repair.ID)
	if rec := f.post(repairPath+"/archive", tech, url.Values{"csrf_token": {csrf}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("archive repair: %d %s", rec.Code, rec.Body.String())
	}
	if page := f.get(repairPath, tech); !strings.Contains(page, "ticket-unarchive") {
		t.Fatal("archived repair must offer unarchive")
	}
	if page := f.get("/repairs", tech); !strings.Contains(page, "Archive Device") || !strings.Contains(page, "Archived") {
		t.Fatal("archived repair must stay listed with the archived badge")
	}
	if page := f.get("/repairs", tech); strings.Contains(page, "archived-filter") {
		t.Fatal("the archived filter must not be rendered")
	}
	if page := f.get("/", tech); strings.Contains(page, "Archive Device") {
		t.Fatal("archived repair must stay out of dashboard queues")
	}
	if rec := f.post(repairPath+"/unarchive", tech, url.Values{"csrf_token": {csrf}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("unarchive repair: %d", rec.Code)
	}

	viewer := f.login("viewer", "password12345")
	viewerCSRF := f.csrfFor(viewer)
	if rec := f.post(customerPath+"/archive", viewer, url.Values{"csrf_token": {viewerCSRF}}); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer archive must be forbidden: %d", rec.Code)
	}
}

func TestDashboardQueuesInWeb(t *testing.T) {
	f := newWeb(t)
	tech := f.login("tech", "password12345")
	c, err := f.svc.CreateCustomer(context.Background(), "setup", app.CustomerInput{Name: "Queue Owner"})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := f.svc.CreateRepair(context.Background(), "setup", app.RepairInput{CustomerID: c.ID, Device: "Ready Device", Issue: "Done"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetRepairStage(context.Background(), "setup", ready.ID, "in_progress", false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetRepairStage(context.Background(), "setup", ready.ID, "repaired", false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateRepair(context.Background(), "setup", app.RepairInput{CustomerID: c.ID, Device: "Open Device", Issue: "Open"}); err != nil {
		t.Fatal(err)
	}
	page := f.get("/", tech)
	for _, want := range []string{"Workbench", "Ready for collection", "Open Device", "Ready Device", "1 open", "1 waiting"} {
		if !strings.Contains(page, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

// ---- full web workflow ---------------------------------------------------

func TestFullWebWorkflow(t *testing.T) {
	f := newWeb(t)
	tech := f.login("tech", "password12345")
	csrf := f.csrfFor(tech)

	// Customer.
	if rec := f.post("/customers/new", tech, url.Values{
		"name": {"Web Customer"}, "phone": {"123"}, "csrf_token": {csrf},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("customer create: %d", rec.Code)
	}
	// Repair (with idempotency key — same shared service contract).
	if rec := f.post("/repairs/new", tech, url.Values{
		"customer_id": {"1"}, "device": {"Web Device"}, "issue": {"broken"},
		"idempotency_key": {"web-req-0001"}, "csrf_token": {csrf},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("repair create: %d", rec.Code)
	}
	repairPage := f.get("/repairs/1", tech)
	for _, want := range []string{"Web Device", "stage", "Repair log", "gallery"} {
		if !strings.Contains(repairPage, want) {
			t.Fatalf("repair page missing %q", want)
		}
	}

	// Entry + stage + part + estimate.
	if rec := f.post("/repairs/1/entries", tech, url.Values{
		"kind": {"work"}, "body": {"web entry"}, "csrf_token": {csrf},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("entry: %d", rec.Code)
	}
	if rec := f.post("/repairs/1/stage", tech, url.Values{
		"stage": {"in_progress"}, "csrf_token": {csrf},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("stage: %d", rec.Code)
	}
	if rec := f.post("/repairs/1/parts", tech, url.Values{
		"description": {"Screen"}, "quantity": {"1.5"}, "unit": {"pcs"},
		"unit_price": {"10.00"}, "csrf_token": {csrf},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("part: %d", rec.Code)
	}
	if rec := f.post("/repairs/1/estimates", tech, url.Values{
		"note": {"web estimate"}, "discount_percent": {"10"}, "csrf_token": {csrf},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("estimate: %d", rec.Code)
	}
	page := f.get("/repairs/1", tech)
	for _, want := range []string{"web entry", "in_progress", "Screen", "estimate", "pickup-heading", "activity-heading", "not collected"} {
		if !strings.Contains(page, want) {
			t.Fatalf("repair page missing %q after mutations (truncated render?)", want)
		}
	}
	if strings.Contains(page, "internal rendering error") {
		t.Fatal("template execution failed mid-page")
	}

	// Attachment upload (multipart) + byte-exact download.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "photo ünïcode.jpg")
	fw.Write([]byte{0xff, 0xd8, 0xff, 0x00, 0x01, 0xfe, 0xff})
	mw.WriteField("caption", "web upload")
	mw.WriteField("csrf_token", csrf)
	mw.Close()
	req := httptest.NewRequest("POST", "http://web.test/repairs/1/attachments", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", "http://web.test")
	req.AddCookie(tech)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}

	dlReq := httptest.NewRequest("GET", "http://web.test/attachments/1/download", nil)
	dlReq.AddCookie(tech)
	dlRec := httptest.NewRecorder()
	f.handler.ServeHTTP(dlRec, dlReq)
	if dlRec.Code != http.StatusOK {
		t.Fatalf("download: %d", dlRec.Code)
	}
	got := dlRec.Body.Bytes()
	if !bytes.Equal(got, []byte{0xff, 0xd8, 0xff, 0x00, 0x01, 0xfe, 0xff}) {
		t.Fatal("downloaded bytes differ from upload")
	}
	// JPEG magic => preview-safe => served inline.
	if cd := dlRec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "inline") {
		t.Fatalf("preview-safe jpeg must serve inline, got %q", cd)
	}
	// A non-image upload must be download-only, never rendered inline.
	var body2 bytes.Buffer
	mw2 := multipart.NewWriter(&body2)
	fw2, _ := mw2.CreateFormFile("file", "firmware.bin")
	fw2.Write([]byte{0x7f, 'E', 'L', 'F', 0x00, 0x01})
	mw2.WriteField("csrf_token", csrf)
	mw2.Close()
	req2 := httptest.NewRequest("POST", "http://web.test/repairs/1/attachments", &body2)
	req2.Header.Set("Content-Type", mw2.FormDataContentType())
	req2.Header.Set("Origin", "http://web.test")
	req2.AddCookie(tech)
	rec2 := httptest.NewRecorder()
	f.handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusSeeOther {
		t.Fatalf("binary upload: %d", rec2.Code)
	}
	dl2 := httptest.NewRequest("GET", "http://web.test/attachments/2/download", nil)
	dl2.AddCookie(tech)
	dl2Rec := httptest.NewRecorder()
	f.handler.ServeHTTP(dl2Rec, dl2)
	if cd := dl2Rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Fatalf("binary must download, got %q", cd)
	}
	if ct := dl2Rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("binary content type: %q", ct)
	}

	// Caption edit via web.
	if rec := f.post("/attachments/1/caption", tech, url.Values{
		"caption": {"edited via web"}, "csrf_token": {csrf},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("caption: %d", rec.Code)
	}
	// Detach (recoverable) then admin purge after grace with explicit confirm.
	if rec := f.post("/attachments/1/detach", tech, url.Values{"csrf_token": {csrf}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("detach: %d", rec.Code)
	}
	admin := f.login("admin", "password12345")
	adminCSRF := f.csrfFor(admin)
	if rec := f.post("/attachments/1/purge", admin, url.Values{"confirm": {"1"}, "csrf_token": {adminCSRF}}); rec.Code == http.StatusSeeOther {
		t.Fatal("purge within grace period must be refused")
	}
	// Jump past the retention grace. Note: advancing the service clock also
	// expires sessions created earlier (absolute TTL) — sign in afirst, as a
	// real operator would after the time passed.
	f.svc.SetClock(func() time.Time { return time.Now().Add(31 * 24 * time.Hour) })
	admin = f.login("admin", "password12345")
	adminCSRF = f.csrfFor(admin)
	if rec := f.post("/attachments/1/purge", admin, url.Values{"confirm": {"1"}, "csrf_token": {adminCSRF}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("purge after grace: %d", rec.Code)
	}

	// CLI/web parity: the shared service sees the same records.
	p, _ := f.svc.ListRepairAttachments(context.Background(), 1, true)
	if len(p) != 2 || p[0].State != "purged" || p[1].State != "attached" {
		t.Fatalf("parity check: %+v", p)
	}
}

func TestContactAndDeviceFieldsInWeb(t *testing.T) {
	f := newWeb(t)
	tech := f.login("tech", "password12345")
	csrf := f.csrfFor(tech)
	if rec := f.post("/customers/new", tech, url.Values{
		"name": {"Parity Owner"}, "address": {"1 Test Street"}, "csrf_token": {csrf},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("customer create: %d", rec.Code)
	}
	if rec := f.post("/repairs/new", tech, url.Values{
		"customer_id": {"1"}, "device": {"Laptop"}, "brand": {"Lenovo"}, "model": {"T480"},
		"serial": {"SN-1"}, "issue": {"No power"}, "csrf_token": {csrf},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("repair create: %d %s", rec.Code, rec.Body.String())
	}
	page := f.get("/repairs/1", tech)
	for _, want := range []string{"Laptop", "Lenovo", "T480", "SN-1", "No power"} {
		if !strings.Contains(page, want) {
			t.Errorf("repair page missing %q", want)
		}
	}
	customer := f.get("/customers/1", tech)
	if !strings.Contains(customer, "1 Test Street") {
		t.Fatal("customer address missing from detail form")
	}

	detail, err := f.svc.GetRepair(context.Background(), 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if rec := f.post("/repairs/1", tech, url.Values{
		"device": {"Laptop"}, "brand": {"Lenovo"}, "model": {"T480"}, "serial": {"SN-2"},
		"issue": {"No power"}, "notes": {""},
		"revision": {detail.Revision}, "csrf_token": {csrf},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("device edit: %d %s", rec.Code, rec.Body.String())
	}
	if page := f.get("/repairs/1", tech); !strings.Contains(page, "SN-2") {
		t.Fatal("edited serial missing from repair page")
	}
}

func TestRepairDetailsEdit(t *testing.T) {
	f := newWeb(t)
	tech := f.login("tech", "password12345")
	csrf := f.csrfFor(tech)

	c, err := f.svc.CreateCustomer(context.Background(), "setup", app.CustomerInput{Name: "Details Owner"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := f.svc.CreateRepair(context.Background(), "setup", app.RepairInput{CustomerID: c.ID, Device: "Old device", Issue: "Old issue"})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := f.svc.GetRepair(context.Background(), tk.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	rec := f.post("/repairs/"+itoaLocal(tk.ID), tech, url.Values{
		"device": {"Edited device"}, "issue": {"Edited issue"}, "notes": {"Edited notes"},
		"revision": {detail.Revision}, "csrf_token": {csrf},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("repair details edit: %d %s", rec.Code, rec.Body.String())
	}
	page := f.get("/repairs/"+itoaLocal(tk.ID), tech)
	for _, want := range []string{"Edited device", "Edited issue", "Edited notes"} {
		if !strings.Contains(page, want) {
			t.Fatalf("repair page missing %q after details edit", want)
		}
	}
}

func itoaLocal(v int64) string { return strconv.FormatInt(v, 10) }

func TestCustomerDraftRetainedOnValidation(t *testing.T) {
	f := newWeb(t)
	tech := f.login("tech", "password12345")
	csrf := f.csrfFor(tech)

	rec := f.post("/customers/new", tech, url.Values{
		"name": {""}, "phone": {"555-draft"}, "notes": {"keep this draft"},
		"csrf_token": {csrf},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("validation must return the form with 400, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"555-draft", "keep this draft", "name must not be empty"} {
		if !strings.Contains(body, want) {
			t.Fatalf("draft response missing %q", want)
		}
	}
}

func TestCustomerConflictReviewAndRetry(t *testing.T) {
	f := newWeb(t)
	tech := f.login("tech", "password12345")
	csrf := f.csrfFor(tech)

	c, err := f.svc.CreateCustomer(context.Background(), "setup", app.CustomerInput{Name: "Original Name"})
	if err != nil {
		t.Fatal(err)
	}
	oldRevision := c.Revision
	other := "Other Writer"
	if _, err := f.svc.UpdateCustomer(context.Background(), "setup", c.ID, app.CustomerPatch{Name: &other}, oldRevision); err != nil {
		t.Fatal(err)
	}

	rec := f.post("/customers/"+strconv.FormatInt(c.ID, 10), tech, url.Values{
		"name": {"My Draft"}, "phone": {"555"}, "email": {""}, "notes": {"draft notes"},
		"revision": {oldRevision}, "csrf_token": {csrf},
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale update must return 409, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"My Draft", "draft notes", "Current saved values", "Other Writer"} {
		if !strings.Contains(body, want) {
			t.Fatalf("conflict response missing %q", want)
		}
	}

	current, err := f.svc.GetCustomer(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	rec = f.post("/customers/"+strconv.FormatInt(c.ID, 10), tech, url.Values{
		"name": {"My Draft"}, "phone": {"555"}, "email": {""}, "notes": {"draft notes"},
		"revision": {current.Revision}, "csrf_token": {csrf},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("reviewed retry must save, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestRepairDraftRetainedOnValidation(t *testing.T) {
	f := newWeb(t)
	tech := f.login("tech", "password12345")
	csrf := f.csrfFor(tech)
	c, err := f.svc.CreateCustomer(context.Background(), "setup", app.CustomerInput{Name: "Repair Owner"})
	if err != nil {
		t.Fatal(err)
	}

	rec := f.post("/repairs/new", tech, url.Values{
		"customer_id": {strconv.FormatInt(c.ID, 10)}, "device": {""}, "issue": {"draft issue"},
		"notes": {"draft notes"}, "csrf_token": {csrf},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("validation must return the form with 400, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"draft issue", "draft notes", "device must not be empty"} {
		if !strings.Contains(body, want) {
			t.Fatalf("repair draft response missing %q", want)
		}
	}
}

func TestRepairConflictReviewAndEntryDraft(t *testing.T) {
	f := newWeb(t)
	tech := f.login("tech", "password12345")
	csrf := f.csrfFor(tech)
	c, err := f.svc.CreateCustomer(context.Background(), "setup", app.CustomerInput{Name: "Repair Owner"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := f.svc.CreateRepair(context.Background(), "setup", app.RepairInput{
		CustomerID: c.ID, Device: "Original Device", Issue: "Original issue",
	})
	if err != nil {
		t.Fatal(err)
	}
	oldRevision := tk.Revision
	otherDevice := "Other Device"
	if _, err := f.svc.UpdateRepairDetails(context.Background(), "setup", tk.ID, app.RepairPatch{Device: &otherDevice}, oldRevision); err != nil {
		t.Fatal(err)
	}

	rec := f.post("/repairs/"+strconv.FormatInt(tk.ID, 10), tech, url.Values{
		"device": {"Draft Device"}, "issue": {"Draft issue"}, "notes": {"Draft notes"},
		"revision": {oldRevision}, "csrf_token": {csrf},
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale repair update must return 409, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Draft Device", "Draft notes", "Current saved values", "Other Device"} {
		if !strings.Contains(body, want) {
			t.Fatalf("repair conflict response missing %q", want)
		}
	}

	current, err := f.svc.GetRepair(context.Background(), tk.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	rec = f.post("/repairs/"+strconv.FormatInt(tk.ID, 10), tech, url.Values{
		"device": {"Draft Device"}, "issue": {"Draft issue"}, "notes": {"Draft notes"},
		"revision": {current.Revision}, "csrf_token": {csrf},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("reviewed repair retry must save, got %d", rec.Code)
	}

	// A refused log entry keeps the submitted body and selected kind.
	rec = f.post("/repairs/"+strconv.FormatInt(tk.ID, 10)+"/entries", tech, url.Values{
		"kind": {"nonsense"}, "body": {"entry draft marker"}, "csrf_token": {csrf},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid entry kind must return 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "entry draft marker") {
		t.Fatal("refused log entry must retain the draft body")
	}
}

func TestEntryRevisionHistoryAndRestore(t *testing.T) {
	f := newWeb(t)
	tech := f.login("tech", "password12345")
	csrf := f.csrfFor(tech)
	c, err := f.svc.CreateCustomer(context.Background(), "setup", app.CustomerInput{Name: "Log Owner"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := f.svc.CreateRepair(context.Background(), "setup", app.RepairInput{
		CustomerID: c.ID, Device: "Log Device", Issue: "Log issue",
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := f.svc.AddRepairEntry(context.Background(), "tech", tk.ID, "work", "original entry text")
	if err != nil {
		t.Fatal(err)
	}
	newBody := "corrected entry text"
	if _, err := f.svc.EditRepairEntry(context.Background(), "tech", entry.ID, nil, &newBody); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.DeleteRepairEntry(context.Background(), "tech", entry.ID); err != nil {
		t.Fatal(err)
	}

	page := f.get("/repairs/"+strconv.FormatInt(tk.ID, 10), tech)
	for _, want := range []string{"Deleted entries (1)", "original entry text", "Restore entry #"} {
		if !strings.Contains(page, want) {
			t.Fatalf("deleted-entry view missing %q", want)
		}
	}

	rec := f.post("/repairs/"+strconv.FormatInt(tk.ID, 10)+"/entries/"+strconv.FormatInt(entry.ID, 10)+"/restore", tech, url.Values{"csrf_token": {csrf}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("restore: %d %s", rec.Code, rec.Body.String())
	}
	page = f.get("/repairs/"+strconv.FormatInt(tk.ID, 10), tech)
	if strings.Contains(page, "Deleted entries") {
		t.Fatal("restored entry must leave the deleted list")
	}
	for _, want := range []string{"corrected entry text", "Revision history (1)", "original entry text"} {
		if !strings.Contains(page, want) {
			t.Fatalf("restored entry view missing %q", want)
		}
	}
}

func TestEstimateAcceptInWeb(t *testing.T) {
	f := newWeb(t)
	tech := f.login("tech", "password12345")
	csrf := f.csrfFor(tech)
	c, err := f.svc.CreateCustomer(context.Background(), "setup", app.CustomerInput{Name: "Quote Owner"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := f.svc.CreateRepair(context.Background(), "setup", app.RepairInput{
		CustomerID: c.ID, Device: "Quote Device", Issue: "Quote issue",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AddRepairPart(context.Background(), "setup", tk.ID, app.PartInput{
		Description: "Replacement screen", QuantityMilli: 1000, Unit: "pcs", UnitPriceCents: 8499,
	}); err != nil {
		t.Fatal(err)
	}
	estimate, err := f.svc.CreateEstimate(context.Background(), "setup", tk.ID, app.EstimateInput{})
	if err != nil {
		t.Fatal(err)
	}

	path := "/repairs/" + strconv.FormatInt(tk.ID, 10)
	page := f.get(path, tech)
	if !strings.Contains(page, "Line items") || !strings.Contains(page, "Replacement screen") {
		t.Fatal("estimate line details must be available in the web UI")
	}
	rec := f.post(path+"/estimates/"+strconv.FormatInt(estimate.ID, 10)+"/accept", tech, url.Values{"csrf_token": {csrf}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("accept estimate: %d %s", rec.Code, rec.Body.String())
	}
	page = f.get(path, tech)
	if !strings.Contains(page, "accepted") {
		t.Fatal("accepted estimate must be visible on the repair page")
	}
}

func TestPartLifecycleInWeb(t *testing.T) {
	f := newWeb(t)
	tech := f.login("tech", "password12345")
	csrf := f.csrfFor(tech)
	c, err := f.svc.CreateCustomer(context.Background(), "setup", app.CustomerInput{Name: "Part Owner"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := f.svc.CreateRepair(context.Background(), "setup", app.RepairInput{
		CustomerID: c.ID, Device: "Part Device", Issue: "Part issue",
	})
	if err != nil {
		t.Fatal(err)
	}
	part, err := f.svc.AddRepairPart(context.Background(), "setup", tk.ID, app.PartInput{
		Description: "Original screen", QuantityMilli: 1000, Unit: "pcs", UnitPriceCents: 5000,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "/repairs/" + strconv.FormatInt(tk.ID, 10)
	partPath := path + "/parts/" + strconv.FormatInt(part.ID, 10)

	rec := f.post(partPath, tech, url.Values{
		"description": {"Edited screen"}, "quantity": {"1.5"}, "unit": {"pcs"},
		"unit_cost": {"10.00"}, "unit_price": {"84.99"},
		"revision": {part.Revision}, "csrf_token": {csrf},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("part edit: %d %s", rec.Code, rec.Body.String())
	}
	page := f.get(path, tech)
	if !strings.Contains(page, "Edited screen") || !strings.Contains(page, "127.49") {
		t.Fatalf("edited part/total not rendered: %s", page)
	}

	if rec := f.post(partPath+"/remove", tech, url.Values{"csrf_token": {csrf}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("part remove: %d", rec.Code)
	}
	page = f.get(path, tech)
	if !strings.Contains(page, "Deleted parts (1)") || !strings.Contains(page, "part-restore-") {
		t.Fatal("deleted part must appear in the recovery section")
	}

	if rec := f.post(partPath+"/restore", tech, url.Values{"csrf_token": {csrf}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("part restore: %d", rec.Code)
	}
	page = f.get(path, tech)
	if strings.Contains(page, "Deleted parts") || !strings.Contains(page, "Edited screen") {
		t.Fatal("restored part must return to the active list")
	}
}

func TestAttachmentLifecycleInWeb(t *testing.T) {
	f := newWeb(t)
	tech := f.login("tech", "password12345")
	csrf := f.csrfFor(tech)
	c, err := f.svc.CreateCustomer(context.Background(), "setup", app.CustomerInput{Name: "Media Owner"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := f.svc.CreateRepair(context.Background(), "setup", app.RepairInput{
		CustomerID: c.ID, Device: "Media Device", Issue: "Media issue",
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := f.svc.AddRepairEntry(context.Background(), "setup", tk.ID, "work", "media entry")
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := f.svc.AddAttachment(context.Background(), "setup", tk.ID, 0,
		"test.bin", strings.NewReader("test bytes"), "", "test")
	if err != nil {
		t.Fatal(err)
	}

	if rec := f.post("/attachments/"+strconv.FormatInt(attachment.ID, 10)+"/link", tech, url.Values{
		"entry_id": {strconv.FormatInt(entry.ID, 10)}, "csrf_token": {csrf},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("link: %d %s", rec.Code, rec.Body.String())
	}
	page := f.get("/repairs/"+strconv.FormatInt(tk.ID, 10), tech)
	if !strings.Contains(page, "Log links (1)") {
		t.Fatal("linked attachment must show its entry count")
	}

	if rec := f.post("/attachments/"+strconv.FormatInt(attachment.ID, 10)+"/unlink", tech, url.Values{
		"entry_id": {strconv.FormatInt(entry.ID, 10)}, "csrf_token": {csrf},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("unlink: %d %s", rec.Code, rec.Body.String())
	}

	if rec := f.post("/attachments/"+strconv.FormatInt(attachment.ID, 10)+"/detach", tech, url.Values{"csrf_token": {csrf}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("detach: %d", rec.Code)
	}
	page = f.get("/repairs/"+strconv.FormatInt(tk.ID, 10), tech)
	if !strings.Contains(page, "Detached files (1)") {
		t.Fatal("detached file must appear in the recovery section")
	}
	if rec := f.post("/attachments/"+strconv.FormatInt(attachment.ID, 10)+"/restore", tech, url.Values{"csrf_token": {csrf}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("restore: %d", rec.Code)
	}
	page = f.get("/repairs/"+strconv.FormatInt(tk.ID, 10), tech)
	if strings.Contains(page, "Detached files") || !strings.Contains(page, "test.bin") {
		t.Fatal("restored file must return to the gallery")
	}
}

func TestPartAndEstimateDraftsRetained(t *testing.T) {
	f := newWeb(t)
	tech := f.login("tech", "password12345")
	csrf := f.csrfFor(tech)
	c, err := f.svc.CreateCustomer(context.Background(), "setup", app.CustomerInput{Name: "Draft Owner"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := f.svc.CreateRepair(context.Background(), "setup", app.RepairInput{
		CustomerID: c.ID, Device: "Draft Device", Issue: "Draft issue",
	})
	if err != nil {
		t.Fatal(err)
	}
	part, err := f.svc.AddRepairPart(context.Background(), "setup", tk.ID, app.PartInput{
		Description: "Original", QuantityMilli: 1000, Unit: "pcs", UnitPriceCents: 5000,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "/repairs/" + strconv.FormatInt(tk.ID, 10)

	// Invalid quantity: preserve all add-form fields and show the error.
	rec := f.post(path+"/parts", tech, url.Values{
		"description": {"Draft part"}, "quantity": {"nope"}, "unit": {"pcs"},
		"unit_cost": {"10.00"}, "unit_price": {"84.99"}, "csrf_token": {csrf},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid part add: %d", rec.Code)
	}
	for _, want := range []string{"Draft part", "84.99", "quantity"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("part draft response missing %q", want)
		}
	}

	// Invalid edit: preserve the submitted values in that part's disclosure.
	rec = f.post(path+"/parts/"+strconv.FormatInt(part.ID, 10), tech, url.Values{
		"description": {"Edited draft"}, "quantity": {"bad"}, "unit": {"pcs"},
		"unit_cost": {""}, "unit_price": {"90.00"}, "revision": {part.Revision},
		"csrf_token": {csrf},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid part edit: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Edited draft") || !strings.Contains(rec.Body.String(), `value="bad"`) {
		t.Fatal("part edit draft must be retained")
	}

	// Invalid discount: preserve estimate fields.
	rec = f.post(path+"/estimates", tech, url.Values{
		"note": {"draft estimate"}, "discount_percent": {"101"}, "csrf_token": {csrf},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid estimate: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "draft estimate") || !strings.Contains(rec.Body.String(), `value="101"`) {
		t.Fatal("estimate draft must be retained")
	}
}

func TestCaptionDraftRetained(t *testing.T) {
	f := newWeb(t)
	tech := f.login("tech", "password12345")
	csrf := f.csrfFor(tech)
	c, err := f.svc.CreateCustomer(context.Background(), "setup", app.CustomerInput{Name: "Caption Owner"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := f.svc.CreateRepair(context.Background(), "setup", app.RepairInput{
		CustomerID: c.ID, Device: "Caption Device", Issue: "Caption issue",
	})
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := f.svc.AddAttachment(context.Background(), "setup", tk.ID, 0,
		"caption.bin", strings.NewReader("bytes"), "", "test")
	if err != nil {
		t.Fatal(err)
	}
	rec := f.post("/attachments/"+strconv.FormatInt(attachment.ID, 10)+"/caption", tech, url.Values{
		"caption": {strings.Repeat("x", 1001)}, "csrf_token": {csrf},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("long caption: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "caption") {
		t.Fatal("caption validation must render on the repair page")
	}
}

func TestDebugRequestLoggingIsCorrelatedAndRedacted(t *testing.T) {
	var logs bytes.Buffer
	f := newWebWithLogger(t, logging.New(true, &logs))
	cookie := f.login("admin", "password12345")
	csrf := f.csrfFor(cookie)

	req := httptest.NewRequest(http.MethodGet, "http://web.test/settings?token=must-not-be-logged", nil)
	req.AddCookie(cookie)
	req.Header.Set("Authorization", "Bearer must-not-be-logged")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("settings: %d", rec.Code)
	}
	requestID := rec.Header().Get("X-Request-ID")
	if requestID == "" {
		t.Fatal("response must carry a request correlation ID")
	}
	text := logs.String()
	for _, want := range []string{"level=DEBUG", "component=web", "request_id=" + requestID, "status=200", "duration_ms="} {
		if !strings.Contains(text, want) {
			t.Errorf("request log missing %q: %q", want, text)
		}
	}
	for _, secret := range []string{"password12345", cookie.Value, csrf, "must-not-be-logged"} {
		if secret != "" && strings.Contains(text, secret) {
			t.Fatalf("sensitive value leaked into logs: %q", text)
		}
	}
}

func TestNormalModeSuppressesRequestDebugLogs(t *testing.T) {
	var logs bytes.Buffer
	f := newWebWithLogger(t, logging.New(false, &logs))
	f.login("admin", "password12345")
	if strings.Contains(logs.String(), "level=DEBUG") {
		t.Fatalf("normal mode emitted debug logs: %q", logs.String())
	}
}

func TestSecurityHeaders(t *testing.T) {
	f := newWeb(t)
	req := httptest.NewRequest("GET", "http://web.test/login", nil)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	h := rec.Header()
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("security headers missing: %v", h)
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "default-src") {
		t.Fatalf("csp missing: %v", h)
	}
}

// The auth storage contract: no raw passwords, no raw session tokens.
func TestAuthStorageContract(t *testing.T) {
	f := newWeb(t)
	f.login("admin", "password12345") // create one real session
	var hash string
	row := f.svc.DB().QueryRow(`SELECT password_hash FROM users WHERE username='admin'`)
	if err := row.Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("password must be PHC argon2id, got %q", hash)
	}
	if strings.Contains(hash, "password12345") {
		t.Fatal("raw password leaked into storage")
	}
	var tokenHash string
	row = f.svc.DB().QueryRow(`SELECT token_hash FROM auth_sessions LIMIT 1`)
	if err := row.Scan(&tokenHash); err != nil {
		t.Fatal(err)
	}
	if len(tokenHash) != 64 {
		t.Fatalf("session tokens must be stored hashed: %q", tokenHash)
	}

}

// TestAttachmentPreviewBoundAndCaching covers the HTTP boundary: a
// preview-safe image above the bound is not rendered in the gallery and is
// served as a download, the ETag revalidates without re-sending bytes, and the
// stored bytes are untouched.
func TestAttachmentPreviewBoundAndCaching(t *testing.T) {
	f := newWeb(t)
	tech := f.login("tech", "password12345")
	ctx := context.Background()
	c, err := f.svc.CreateCustomer(ctx, "setup", app.CustomerInput{Name: "Preview Bound"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := f.svc.CreateRepair(ctx, "setup", app.RepairInput{CustomerID: c.ID, Device: "Dev", Issue: "Issue"})
	if err != nil {
		t.Fatal(err)
	}

	small := append([]byte("\xff\xd8\xff\xe0"), bytes.Repeat([]byte{0x22}, 64)...)
	if _, err := f.svc.AddAttachment(ctx, "setup", tk.ID, 0, "small.jpg", bytes.NewReader(small), "", "test"); err != nil {
		t.Fatal(err)
	}
	big := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x00}, int(app.PreviewMaxBytes))...)
	bigAtt, err := f.svc.AddAttachment(ctx, "setup", tk.ID, 0, "big.png", bytes.NewReader(big), "", "test")
	if err != nil {
		t.Fatal(err)
	}

	page := f.get("/repairs/"+strconv.FormatInt(tk.ID, 10), tech)
	if got := strings.Count(page, `<img class="thumb"`); got != 1 {
		t.Fatalf("gallery must render exactly the bounded image inline, got %d thumbnails", got)
	}
	if !strings.Contains(page, "attachment-"+strconv.FormatInt(bigAtt.ID, 10)) {
		t.Fatal("oversized attachment must still be listed")
	}

	path := "/attachments/" + strconv.FormatInt(bigAtt.ID, 10) + "/download"
	get := func(ifNoneMatch string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "http://web.test"+path, nil)
		req.AddCookie(tech)
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		return rec
	}
	rec := get("")
	if rec.Code != http.StatusOK {
		t.Fatalf("download: %d", rec.Code)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Fatalf("oversized preview-safe image must download, got %q", cd)
	}
	if !bytes.Equal(rec.Body.Bytes(), big) {
		t.Fatal("download must return the original bytes unchanged")
	}
	etag := rec.Header().Get("ETag")
	if etag == "" || !strings.Contains(rec.Header().Get("Cache-Control"), "private") {
		t.Fatalf("expected private caching headers, got ETag=%q Cache-Control=%q",
			etag, rec.Header().Get("Cache-Control"))
	}
	if again := get(etag); again.Code != http.StatusNotModified || again.Body.Len() != 0 {
		t.Fatalf("cached revalidation: %d %d bytes", again.Code, again.Body.Len())
	}
}

func TestLoginThrottleHTTP(t *testing.T) {
	f := newWeb(t)
	form := url.Values{"username": {"admin"}, "password": {"wrong-password"}}
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "http://web.test/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://web.test")
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d", i+1, rec.Code)
		}
	}
	form.Set("password", "password12345")
	req := httptest.NewRequest(http.MethodPost, "http://web.test/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://web.test")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("locked login must return 429, got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("locked login must send Retry-After")
	}
	if strings.Contains(rec.Body.String(), "admin") {
		t.Fatal("throttle response must not reveal the username")
	}
	f.login("tech", "password12345")
}

// TestLegacyTicketPathsRedirectPermanently pins the compatibility shim:
// /tickets… answers 308 to the matching /repairs… path, keeping the method, so
// old links and forms keep working for one release.
func TestLegacyTicketPathsRedirectPermanently(t *testing.T) {
	f := newWeb(t)
	cookie := f.login("tech", "password12345")

	cases := []struct{ method, from, to string }{
		{"GET", "/tickets", "/repairs"},
		{"GET", "/tickets?archived=1", "/repairs?archived=1"},
		{"GET", "/tickets/7", "/repairs/7"},
		{"POST", "/tickets/7/stage", "/repairs/7/stage"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, "http://web.test"+tc.from, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusPermanentRedirect {
			t.Fatalf("%s %s: status %d, want 308", tc.method, tc.from, rec.Code)
		}
		if got := rec.Header().Get("Location"); got != tc.to {
			t.Fatalf("%s %s: Location %q, want %q", tc.method, tc.from, got, tc.to)
		}
	}
}

func TestLiveSearchReturnsOnlyResults(t *testing.T) {
	f := newWeb(t)
	tech := f.login("tech", "password12345")
	ctx := context.Background()
	if _, err := f.svc.CreateCustomer(ctx, "setup", app.CustomerInput{Name: "Live Search Target"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/customers?search=Live&partial=1", "/repairs?search=Live&partial=1"} {
		page := f.get(path, tech)
		if strings.Contains(page, "<html") || strings.Contains(page, "<form") {
			t.Fatalf("%s must return the results fragment only", path)
		}
		if !strings.Contains(page, "id=\"results\"") && !strings.Contains(page, "table-window") {
			t.Fatalf("%s must return the results markup", path)
		}
	}
	if page := f.get("/customers?partial=1", tech); !strings.Contains(page, "Live Search Target") {
		t.Fatal("partial render must include the rows")
	}
	if page := f.get("/customers", tech); !strings.Contains(page, "data-live-search") || !strings.Contains(page, "data-live-results") {
		t.Fatal("list page must opt into live search")
	}
	if page := f.get("/customers", tech); strings.Contains(page, "id=\"search-submit\"") {
		t.Fatal("the search button must be gone (submit stays sr-only)")
	}
}
