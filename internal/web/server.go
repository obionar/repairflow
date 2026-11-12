// Package web is the embedded server-rendered web interface. It is a pure
// adapter: every operation goes through the shared app services; no business
// rules live here. HTML is server-rendered with no required JavaScript; all
// assets are embedded. See docs/SECURITY.md for the trust boundary.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"repairflow/internal/app"
	"repairflow/internal/errs"
	"repairflow/internal/i18n"
	"repairflow/internal/logging"
)

//go:embed templates static
var embedded embed.FS

// Server hosts one workspace over HTTP.
type Server struct {
	svc          *app.Service
	tmpl         *template.Template
	logger       *slog.Logger
	tr           *i18n.Translator
	language     string
	authRequired bool
	loopbackOnly bool
	loginLimit   *loginThrottle
}

// languageCookie overrides the workspace UI language for one browser.
const languageCookie = "repairflow_lang"

// New builds the web server over the shared application service.
func New(svc *app.Service) (*Server, error) {
	tmpl, err := parseAll()
	if err != nil {
		return nil, fmt.Errorf("web: cannot parse templates: %w", err)
	}
	tr, err := i18n.New()
	if err != nil {
		return nil, err
	}
	return &Server{svc: svc, tmpl: tmpl, logger: logging.New(false, os.Stderr).With("component", "web"),
		tr: tr, language: "en", authRequired: true, loginLimit: newLoginThrottle()}, nil
}

// WithAuthentication configures whether browser login is required. The
// config package restricts disabling it to loopback listeners.
func (s *Server) WithAuthentication(required bool) *Server {
	s.authRequired = required
	return s
}

// WithTranslations installs the catalog set (embedded plus any workspace
// locales/ overrides) and the workspace default language.
func (s *Server) WithTranslations(tr *i18n.Translator, defaultLanguage string) *Server {
	if tr != nil {
		s.tr = tr
	}
	s.language = i18n.Normalize(defaultLanguage)
	return s
}

// WithLoopbackOnly rejects requests whose Host is not loopback/localhost.
// serve enables this unless web.allow_remote is explicitly configured.
func (s *Server) WithLoopbackOnly(enabled bool) *Server {
	s.loopbackOnly = enabled
	return s
}

// WithLogger attaches the process's shared diagnostic configuration.
func (s *Server) WithLogger(logger *slog.Logger) *Server {
	if logger != nil {
		s.logger = logger.With("component", "web")
	}
	return s
}

// contentSecurityPolicy allows only same-origin assets. Templates carry no
// inline styles or scripts, so this needs no unsafe-inline escape hatch.
const contentSecurityPolicy = "default-src 'self'; img-src 'self'; style-src 'self'"

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"cents":         app.FormatCents,
		"milli":         app.FormatQuantityMilli,
		"lineTotal":     func(q, p int64) string { return app.FormatCents(app.LineTotalCents(q, p)) },
		"humanize":      humanize,
		"stageLabel":    humanize,
		"kindLabel":     humanize,
		"date":          func(raw string) string { return formatTime(raw, "2006-01-02") },
		"when":          func(raw string) string { return formatTime(raw, "2006-01-02 15:04") },
		"bytes":         humanBytes,
		"detail":        describeDetail,
		"terminalStage": app.IsTerminalStage,
	}
}

// humanize renders snake_case machine values as short human labels.
func humanize(value string) string {
	value = strings.ReplaceAll(value, "_", " ")
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

// formatTime renders RFC3339 application timestamps; unknown formats are
// passed through unchanged rather than hidden.
func formatTime(raw, layout string) string {
	for _, candidate := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(candidate, raw); err == nil {
			return parsed.Format(layout)
		}
	}
	return raw
}

// describeDetail turns a stored activity detail JSON object into a short,
// stable human line. Raw JSON (and long hashes) must never reach the page.
func describeDetail(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return raw
	}
	value := func(key string) (any, bool) { v, ok := obj[key]; return v, ok }
	if from, ok := value("from"); ok {
		if to, ok := value("to"); ok {
			return fmt.Sprintf("%v → %v", from, to)
		}
	}
	if id, ok := value("entry_id"); ok {
		if kind, ok := value("kind"); ok {
			return fmt.Sprintf("entry #%v · %s", id, humanize(fmt.Sprint(kind)))
		}
		return fmt.Sprintf("entry #%v", id)
	}
	if id, ok := value("part_id"); ok {
		if desc, ok := value("description"); ok {
			return fmt.Sprintf("part #%v · %v", id, desc)
		}
		return fmt.Sprintf("part #%v", id)
	}
	if id, ok := value("attachment_id"); ok {
		if name, ok := value("display_name"); ok {
			return fmt.Sprintf("file #%v · %v", id, name)
		}
		return fmt.Sprintf("file #%v", id)
	}
	if id, ok := value("estimate_id"); ok {
		return fmt.Sprintf("estimate #%v", id)
	}
	keys := make([]string, 0, len(obj))
	for key := range obj {
		if key != "sha256" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s %v", strings.ReplaceAll(key, "_", " "), obj[key]))
	}
	return strings.Join(parts, " · ")
}

// humanBytes renders an upload limit or file size for humans.
func humanBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGTPE"[exp])
}

// ListenAndServe runs the web interface until ctx is cancelled, then shuts
// down gracefully.
func ListenAndServe(ctx context.Context, host string, port int, handler http.Handler) error {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return errs.Wrap(errs.CodeInternal, err, "web server failed on %s", addr)
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return errs.Wrap(errs.CodeInternal, err, "web server shutdown failed on %s", addr)
		}
		return nil
	}
}

// Assets returns the embedded static tree (used by tests and future needs).
func Assets() fs.FS {
	sub, _ := fs.Sub(embedded, "static")
	return sub
}

// Handler returns the fully wired HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(mustSub())))
	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.loginSubmit)
	mux.HandleFunc("POST /logout", s.authed(s.logout, app.RoleViewer))

	mux.HandleFunc("GET /{$}", s.authed(s.dashboard, app.RoleViewer))
	mux.HandleFunc("GET /customers", s.authed(s.customerList, app.RoleViewer))
	mux.HandleFunc("GET /customers/new", s.authed(s.customerNew, app.RoleTechnician))
	mux.HandleFunc("POST /customers/new", s.authed(s.customerCreate, app.RoleTechnician))
	mux.HandleFunc("GET /customers/{id}", s.authed(s.customerShow, app.RoleViewer))
	mux.HandleFunc("POST /customers/{id}", s.authed(s.customerUpdate, app.RoleTechnician))
	mux.HandleFunc("POST /customers/{id}/archive", s.authed(s.customerArchive, app.RoleTechnician))
	mux.HandleFunc("POST /customers/{id}/unarchive", s.authed(s.customerUnarchive, app.RoleTechnician))

	// Legacy ticket paths: 308 keeps methods and bodies,
	// so old links and forms keep working for one release.
	mux.HandleFunc("/tickets", repairRedirect)
	mux.HandleFunc("/tickets/", repairRedirect)

	mux.HandleFunc("GET /repairs", s.authed(s.repairList, app.RoleViewer))
	mux.HandleFunc("GET /repairs/new", s.authed(s.repairNew, app.RoleTechnician))
	mux.HandleFunc("POST /repairs/new", s.authed(s.repairCreate, app.RoleTechnician))
	mux.HandleFunc("GET /repairs/{id}", s.authed(s.repairShow, app.RoleViewer))
	mux.HandleFunc("POST /repairs/{id}", s.authed(s.repairUpdate, app.RoleTechnician))
	mux.HandleFunc("POST /repairs/{id}/archive", s.authed(s.repairArchive, app.RoleTechnician))
	mux.HandleFunc("POST /repairs/{id}/unarchive", s.authed(s.repairUnarchive, app.RoleTechnician))
	mux.HandleFunc("POST /repairs/{id}/stage", s.authed(s.repairStage, app.RoleTechnician))
	mux.HandleFunc("POST /repairs/{id}/entries", s.authed(s.repairEntryAdd, app.RoleTechnician))
	mux.HandleFunc("POST /repairs/{id}/entries/{eid}/edit", s.authed(s.repairEntryEdit, app.RoleTechnician))
	mux.HandleFunc("POST /repairs/{id}/entries/{eid}/delete", s.authed(s.repairEntryDelete, app.RoleTechnician))
	mux.HandleFunc("POST /repairs/{id}/entries/{eid}/restore", s.authed(s.repairEntryRestore, app.RoleTechnician))
	mux.HandleFunc("POST /repairs/{id}/pickup", s.authed(s.repairPickup, app.RoleTechnician))
	mux.HandleFunc("POST /repairs/{id}/parts", s.authed(s.repairPartAdd, app.RoleTechnician))
	mux.HandleFunc("POST /repairs/{id}/parts/{pid}", s.authed(s.repairPartUpdate, app.RoleTechnician))
	mux.HandleFunc("POST /repairs/{id}/parts/{pid}/remove", s.authed(s.repairPartRemove, app.RoleTechnician))
	mux.HandleFunc("POST /repairs/{id}/parts/{pid}/restore", s.authed(s.repairPartRestore, app.RoleTechnician))
	mux.HandleFunc("POST /repairs/{id}/estimates", s.authed(s.estimateCreate, app.RoleTechnician))
	mux.HandleFunc("POST /repairs/{id}/estimates/{eid}/accept", s.authed(s.estimateAccept, app.RoleTechnician))

	mux.HandleFunc("POST /repairs/{id}/attachments", s.authed(s.attachmentAdd, app.RoleTechnician))
	mux.HandleFunc("GET /attachments/{id}/download", s.authed(s.attachmentDownload, app.RoleViewer))
	mux.HandleFunc("POST /attachments/{id}/caption", s.authed(s.attachmentCaption, app.RoleTechnician))
	mux.HandleFunc("POST /attachments/{id}/detach", s.authed(s.attachmentDetach, app.RoleTechnician))
	mux.HandleFunc("POST /attachments/{id}/link", s.authed(s.attachmentLink, app.RoleTechnician))
	mux.HandleFunc("POST /attachments/{id}/unlink", s.authed(s.attachmentUnlink, app.RoleTechnician))
	mux.HandleFunc("POST /attachments/{id}/restore", s.authed(s.attachmentRestore, app.RoleTechnician))
	mux.HandleFunc("POST /attachments/{id}/purge", s.authed(s.attachmentPurge, app.RoleAdmin))

	mux.HandleFunc("GET /settings", s.authed(s.settings, app.RoleViewer))
	mux.HandleFunc("POST /settings/password", s.authed(s.settingsPassword, app.RoleViewer))
	mux.HandleFunc("POST /settings/language", s.authed(s.settingsLanguage, app.RoleViewer))
	return s.middleware(mux)
}

func parseAll() (*template.Template, error) {
	return template.New("").Funcs(templateFuncs()).ParseFS(embedded, "templates/*.html")
}

func mustSub() fs.FS {
	sub, _ := fs.Sub(embedded, "static")
	return sub
}
