package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/repository"
)

var calendarLinkPattern = regexp.MustCompile(`http://example\.com/calendar/[A-Za-z0-9_-]{43}/reminders\.ics`)

func (f fixture) fetchFeed(t *testing.T, method, link string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	// Calendar apps fetch without a session cookie.
	f.router.ServeHTTP(w, httptest.NewRequest(method, link, nil))
	return w
}

func (f fixture) createCalendarLink(t *testing.T) string {
	t.Helper()
	w := f.do(t, http.MethodPost, "/calendar", nil, "")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create link: status=%d cache=%q body=%s", w.Code, w.Header().Get("Cache-Control"), w.Body.String())
	}
	link := calendarLinkPattern.FindString(w.Body.String())
	if link == "" {
		t.Fatalf("created page has no link: %s", w.Body.String())
	}
	return link
}

func TestCalendarFeedServesTimeBasedRemindersToLinkHolders(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	types, _ := f.store.ListServiceTypes(t.Context())
	for _, values := range []url.Values{
		{"service_type_id": {types[0].ID.String()}, "months": {"6"}, "miles": {"5000"}, "enabled": {"true"}},
		{"service_type_id": {types[1].ID.String()}, "miles": {"3000"}, "enabled": {"true"}},
		{"service_type_id": {types[2].ID.String()}, "months": {"12"}},
	} {
		if w := f.do(t, http.MethodPost, "/vehicles/"+v.ID.String()+"/reminders", strings.NewReader(values.Encode()), "application/x-www-form-urlencoded"); w.Code != http.StatusSeeOther {
			t.Fatalf("reminder: %d %s", w.Code, w.Body.String())
		}
	}

	page := f.do(t, http.MethodGet, "/calendar", nil, "")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Create calendar link") {
		t.Fatalf("calendar page: %d %s", page.Code, page.Body.String())
	}
	if w := f.fetchFeed(t, http.MethodGet, "http://example.com/calendar/"+strings.Repeat("a", 43)+"/reminders.ics"); w.Code != http.StatusNotFound {
		t.Fatalf("feed before a link exists: %d", w.Code)
	}

	link := f.createCalendarLink(t)
	w := f.fetchFeed(t, http.MethodGet, link)
	body := strings.ReplaceAll(w.Body.String(), "\r\n ", "") // unfold long lines
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/calendar; charset=utf-8" {
		t.Fatalf("feed: status=%d type=%q body=%s", w.Code, w.Header().Get("Content-Type"), body)
	}
	for _, want := range []string{
		"BEGIN:VCALENDAR\r\n",
		"DTSTART;VALUE=DATE:20270131\r\n",
		"DTEND;VALUE=DATE:20270201\r\n",
		"SUMMARY:" + types[0].Name + " due · Dan's Outback\r\n",
		"URL:http://example.com/vehicles/" + v.ID.String() + "/reminders\r\n",
		"END:VCALENDAR\r\n",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("feed missing %q: %s", want, body)
		}
	}
	if count := strings.Count(body, "BEGIN:VEVENT"); count != 1 {
		t.Fatalf("feed has %d events, want only the enabled time-based reminder: %s", count, body)
	}
	f.s.now = func() time.Time { return time.Date(2026, 8, 1, 6, 0, 0, 0, time.UTC) }
	if later := f.fetchFeed(t, http.MethodGet, link); later.Body.String() != w.Body.String() {
		t.Fatalf("an unchanged feed changed between fetches:\n%s\n---\n%s", w.Body.String(), later.Body.String())
	}
	if head := f.fetchFeed(t, http.MethodHead, link); head.Code != http.StatusOK || head.Header().Get("Content-Type") != "text/calendar; charset=utf-8" {
		t.Fatalf("HEAD: %d %q", head.Code, head.Header().Get("Content-Type"))
	}

	page = f.do(t, http.MethodGet, "/calendar", nil, "")
	if !strings.Contains(page.Body.String(), "Reset link") || calendarLinkPattern.MatchString(page.Body.String()) {
		t.Fatalf("calendar page should offer a reset without showing the link again: %s", page.Body.String())
	}

	reset := f.createCalendarLink(t)
	if reset == link {
		t.Fatal("reset kept the same link")
	}
	if w := f.fetchFeed(t, http.MethodGet, link); w.Code != http.StatusNotFound {
		t.Fatalf("old link after reset: %d", w.Code)
	}
	if w := f.fetchFeed(t, http.MethodGet, reset); w.Code != http.StatusOK {
		t.Fatalf("new link after reset: %d", w.Code)
	}

	if w := f.do(t, http.MethodPost, "/calendar/delete", nil, ""); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/calendar" {
		t.Fatalf("turn off: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := f.fetchFeed(t, http.MethodGet, reset); w.Code != http.StatusNotFound {
		t.Fatalf("link after turning off: %d", w.Code)
	}
}

func TestCalendarFeedUsesPublicURLAndRequiresSignInToManage(t *testing.T) {
	f := setup(t)
	f.s.cfg.ReminderEmail.PublicURL = "https://carma.example"
	w := f.do(t, http.MethodPost, "/calendar", nil, "")
	if !strings.Contains(w.Body.String(), `value="https://carma.example/calendar/`) {
		t.Fatalf("link does not use PUBLIC_URL: %s", w.Body.String())
	}
	if w := f.fetchFeed(t, http.MethodGet, "http://example.com/calendar"); w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/login") {
		t.Fatalf("signed-out calendar page: %d %q", w.Code, w.Header().Get("Location"))
	}
	r := httptest.NewRequest(http.MethodPost, "http://example.com/calendar", nil)
	r.Header.Set("Origin", "http://example.com")
	signedOut := httptest.NewRecorder()
	f.router.ServeHTTP(signedOut, r)
	if signedOut.Code != http.StatusSeeOther {
		t.Fatalf("signed-out create: %d", signedOut.Code)
	}
	if feed, err := f.store.GetCalendarFeed(t.Context()); err != nil || feed == nil {
		t.Fatalf("feed=%v err=%v", feed, err)
	}
}

type listVehiclesErrorStore struct {
	repository.Store
}

func (listVehiclesErrorStore) ListVehicles(context.Context, bool) ([]model.Vehicle, error) {
	return nil, errors.New("database unavailable")
}

func TestCreateCalendarLinkKeepsOldLinkWhenPageCannotLoad(t *testing.T) {
	f := setup(t)
	link := f.createCalendarLink(t)
	f.s.store = listVehiclesErrorStore{f.store}
	if w := f.do(t, http.MethodPost, "/calendar", nil, ""); w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", w.Code)
	}
	f.s.store = f.store
	if w := f.fetchFeed(t, http.MethodGet, link); w.Code != http.StatusOK {
		t.Fatalf("a failed reset replaced the link: %d", w.Code)
	}
}
