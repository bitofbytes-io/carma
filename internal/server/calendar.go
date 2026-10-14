package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"

	"github.com/bitofbytes-io/carma/internal/calendar"
	"github.com/bitofbytes-io/carma/internal/middleware"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/go-chi/chi/v5"
)

// calendarTokenLength is the length of a base64url-encoded 32-byte token.
const calendarTokenLength = 43

func (s *Server) calendarPage(response http.ResponseWriter, request *http.Request) {
	data, err := s.calendarData(request)
	if err != nil {
		s.fail(response, err)
		return
	}
	s.render(response, http.StatusOK, "calendar", data)
}

// createCalendarFeed creates the household's calendar link, replacing any
// earlier one, and shows it this once: only its hash is stored.
func (s *Server) createCalendarFeed(response http.ResponseWriter, request *http.Request) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		s.fail(response, err)
		return
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	hash := sha256.Sum256([]byte(token))
	feed := model.CalendarFeed{TokenHash: hash[:], CreatedAt: s.now()}
	if user := middleware.User(request); user != nil {
		feed.CreatedBy = &user.ID
	}
	if err := s.store.ReplaceCalendarFeed(request.Context(), feed); err != nil {
		s.fail(response, err)
		return
	}
	data, err := s.calendarData(request)
	if err != nil {
		s.fail(response, err)
		return
	}
	data.CalendarURL = s.publicOrigin(request) + "/calendar/" + token + "/reminders.ics"
	// The page holds the only copy of the link.
	response.Header().Set("Cache-Control", "no-store")
	s.render(response, http.StatusOK, "calendar", data)
}

func (s *Server) deleteCalendarFeed(response http.ResponseWriter, request *http.Request) {
	if err := s.store.DeleteCalendarFeed(request.Context()); err != nil {
		s.fail(response, err)
		return
	}
	redirectAfterPost(response, request, "/calendar")
}

func (s *Server) calendarData(request *http.Request) (pageData, error) {
	data, err := s.base(request, "Calendar feed")
	if err != nil {
		return data, err
	}
	if data.CalendarFeed, err = s.store.GetCalendarFeed(request.Context()); err == nil && data.CalendarFeed != nil && s.cfg.Location != nil {
		data.CalendarFeed.CreatedAt = data.CalendarFeed.CreatedAt.In(s.cfg.Location)
	}
	return data, err
}

// calendarFeed serves the reminder calendar to anyone holding the current
// link, since calendar apps fetch it without a session.
func (s *Server) calendarFeed(response http.ResponseWriter, request *http.Request) {
	token := chi.URLParam(request, "token")
	if len(token) != calendarTokenLength {
		http.NotFound(response, request)
		return
	}
	feed, err := s.store.GetCalendarFeed(request.Context())
	if err != nil {
		s.fail(response, err)
		return
	}
	hash := sha256.Sum256([]byte(token))
	if feed == nil || subtle.ConstantTimeCompare(hash[:], feed.TokenHash) != 1 {
		http.NotFound(response, request)
		return
	}
	reminders, err := s.store.ListReminders(request.Context(), nil, false)
	if err != nil {
		s.fail(response, err)
		return
	}
	results, err := s.evaluateReminders(request.Context(), reminders)
	if err != nil {
		s.fail(response, err)
		return
	}
	response.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	response.Header().Set("Content-Disposition", `inline; filename="carma-reminders.ics"`)
	response.Header().Set("Cache-Control", "private, no-cache")
	response.Header().Set("X-Robots-Tag", "noindex")
	_, _ = response.Write(calendar.Feed(results, s.now(), s.publicOrigin(request)))
}

// publicOrigin is the origin used in links to Carma: PUBLIC_URL when it is
// configured, otherwise the one the request came in on.
func (s *Server) publicOrigin(request *http.Request) string {
	if s.cfg.ReminderEmail.PublicURL != "" {
		return s.cfg.ReminderEmail.PublicURL
	}
	return middleware.Origin(request)
}
