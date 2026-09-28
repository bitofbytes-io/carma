package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bitofbytes-io/carma/internal/middleware"
	"github.com/bitofbytes-io/carma/internal/reminder"
	"github.com/bitofbytes-io/carma/internal/repository"
)

func (s *Server) base(request *http.Request, title string) (pageData, error) {
	vehicles, err := s.store.ListVehicles(request.Context(), false)
	return pageData{
		Title:         title,
		Authenticated: true,
		User:          middleware.User(request),
		NavVehicles:   vehicles,
	}, err
}

func (s *Server) render(response http.ResponseWriter, status int, name string, data pageData) {
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.WriteHeader(status)
	if err := s.ui.Render(response, name, data); err != nil {
		slog.Error("render", "page", name, "error", err)
	}
}

func (s *Server) renderNamed(response http.ResponseWriter, page, name string, data pageData) {
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.ui.RenderNamed(response, page, name, data); err != nil {
		s.fail(response, err)
	}
}

func (s *Server) fail(response http.ResponseWriter, err error) {
	slog.Error("request failed", "error", err)
	http.Error(response, "Something went wrong", http.StatusInternalServerError)
}

func isHTMX(request *http.Request) bool {
	return strings.EqualFold(request.Header.Get("HX-Request"), "true")
}

// redirectAfterPost answers a completed POST with a 303 redirect, or with an
// HX-Redirect for htmx-issued requests so the browser navigates to target.
func redirectAfterPost(response http.ResponseWriter, request *http.Request, target string) {
	if isHTMX(request) {
		response.Header().Set("HX-Redirect", target)
		response.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(response, request, target, http.StatusSeeOther)
}

func (s *Server) parseMultipart(response http.ResponseWriter, request *http.Request, memory int64) error {
	limit := s.cfg.MaxMultipartBytes
	if limit <= 0 {
		limit = 128 << 20
	}
	request.Body = http.MaxBytesReader(response, request.Body, limit)
	return request.ParseMultipartForm(memory)
}

func (s *Server) multipartError(response http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) || strings.Contains(strings.ToLower(err.Error()), "request body too large") {
		http.Error(response, "Multipart request exceeds the configured total limit", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(response, "Invalid multipart form", http.StatusBadRequest)
}

func (s *Server) notFound(response http.ResponseWriter, err error) {
	if errors.Is(err, repository.ErrNotFound) {
		http.Error(response, "Not found", 404)
	} else {
		s.fail(response, err)
	}
}

// today returns the current calendar date in the configured time zone.
func (s *Server) today() time.Time {
	return reminder.Today(s.now(), s.cfg.Location)
}
