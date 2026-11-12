package web

import (
	"mime"
	"net/http"
	"strings"
	"time"

	"repairflow/internal/errs"
	"repairflow/internal/files"
)

const (
	defaultLoginBodyBytes        int64 = 64 << 10
	defaultFormBodyBytes         int64 = 1 << 20
	defaultAttachmentBodyBytes   int64 = files.MaxAttachmentBytes + (1 << 20)
	defaultLoginReadTimeout            = 15 * time.Second
	defaultFormReadTimeout             = 30 * time.Second
	defaultAttachmentReadTimeout       = 5 * time.Minute
)

type requestBodyPolicy struct {
	maxBytes    int64
	readTimeout time.Duration
}

func (s *Server) requestBodyPolicy(r *http.Request) requestBodyPolicy {
	if r.Method == http.MethodPost && isAttachmentUploadPath(r.URL.Path) {
		return requestBodyPolicy{maxBytes: s.attachmentBodyBytes, readTimeout: s.attachmentReadTimeout}
	}
	if r.URL.Path == "/login" {
		return requestBodyPolicy{maxBytes: s.loginBodyBytes, readTimeout: s.loginReadTimeout}
	}
	return requestBodyPolicy{maxBytes: s.formBodyBytes, readTimeout: s.formReadTimeout}
}

func isAttachmentUploadPath(path string) bool {
	for _, prefix := range []string{"/repairs/", "/tickets/"} {
		if strings.HasPrefix(path, prefix) && strings.HasSuffix(path, "/attachments") {
			return len(path) > len(prefix)+len("/attachments")
		}
	}
	return false
}

// applyRequestBodyLimit installs a hard byte cap before any middleware or
// handler can parse a form. Content-Length is refused early; chunked bodies
// hit MaxBytesReader during parsing instead of spooling without a bound.
func (s *Server) applyRequestBodyLimit(w http.ResponseWriter, r *http.Request) bool {
	policy := s.requestBodyPolicy(r)
	if r.ContentLength > policy.maxBytes {
		s.renderError(w, r, nil, errs.New(errs.CodeValidation, "request body is too large"),
			http.StatusRequestEntityTooLarge)
		return false
	}
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, policy.maxBytes)
	}
	if policy.readTimeout > 0 {
		// httptest writers do not support connection deadlines; real net/http
		// writers do. An unsupported controller is harmless in unit fixtures.
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(policy.readTimeout))
	}
	return true
}

func parseRequestForm(r *http.Request) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err == nil && mediaType == "multipart/form-data" {
		// Keep only small form fields in memory. Large files use the bounded
		// multipart temporary-file path, never an unbounded body read.
		return r.ParseMultipartForm(1 << 20)
	}
	return r.ParseForm()
}
