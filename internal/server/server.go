package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bitofbytes-io/carma/internal/assets"
	"github.com/bitofbytes-io/carma/internal/auth"
	"github.com/bitofbytes-io/carma/internal/config"
	carmaexport "github.com/bitofbytes-io/carma/internal/export"
	"github.com/bitofbytes-io/carma/internal/middleware"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/reminder"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/bitofbytes-io/carma/internal/ui"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

const (
	oauthStateCookie     = "carma_oauth_state"
	loginErrorExpired    = "expired"
	loginErrorOAuth      = "oauth"
	loginErrorNotInvited = "not-invited"
)

type Server struct {
	cfg    *config.Config
	store  repository.Store
	assets assets.Store
	auth   *auth.Service
	google auth.Google
	ui     *ui.Renderer
	now    func() time.Time
}

func New(cfg *config.Config, store repository.Store, assetStore assets.Store, authService *auth.Service, google auth.Google) (*Server, error) {
	renderer, e := ui.New()
	if e != nil {
		return nil, e
	}
	return &Server{cfg: cfg, store: store, assets: assetStore, auth: authService, google: google, ui: renderer, now: time.Now}, nil
}

type pageData struct {
	Title, Error, Flash, Redirect                 string
	Authenticated, Development, Editing, Archived bool
	User                                          *model.User
	NavVehicles, Vehicles                         []model.Vehicle
	Vehicle                                       model.Vehicle
	Record                                        model.Record
	Records                                       []model.Record
	Types                                         []model.ServiceType
	Attachments                                   []model.Attachment
	Reminders, Attention                          []reminder.Result
	Params                                        url.Values
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.RequestID, chimw.RealIP, chimw.Recoverer, middleware.SameOrigin)
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Get("/login", s.loginPage)
	r.Post("/login", s.devLogin)
	r.Get("/api/auth/google", s.oauthStart)
	r.Get("/api/auth/google/callback", s.oauthCallback)
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(s.auth, s.cfg.SecureCookies()))
		r.Post("/logout", s.logout)
		r.Get("/", s.dashboard)
		r.Get("/records", s.allRecords)
		r.Get("/records/export.csv", s.globalCSV)
		r.Get("/vehicles/archived", s.archivedVehicles)
		r.Get("/vehicles/new", s.newVehicle)
		r.Post("/vehicles", s.createVehicle)
		r.Get("/vehicles/{vehicleID}", s.vehicle)
		r.Get("/vehicles/{vehicleID}/edit", s.editVehicle)
		r.Post("/vehicles/{vehicleID}", s.updateVehicle)
		r.Post("/vehicles/{vehicleID}/archive", s.archiveVehicle)
		r.Get("/vehicles/{vehicleID}/photo", s.vehiclePhoto)
		r.Get("/vehicles/{vehicleID}/records/new", s.newRecord)
		r.Post("/vehicles/{vehicleID}/records", s.createRecord)
		r.Get("/vehicles/{vehicleID}/export.csv", s.vehicleCSV)
		r.Get("/vehicles/{vehicleID}/reminders", s.remindersPage)
		r.Post("/vehicles/{vehicleID}/reminders", s.upsertReminder)
		r.Post("/vehicles/{vehicleID}/reminders/{reminderID}/delete", s.deleteReminder)
		r.Get("/records/{recordID}", s.record)
		r.Get("/records/{recordID}/edit", s.editRecord)
		r.Post("/records/{recordID}", s.updateRecord)
		r.Post("/records/{recordID}/delete", s.deleteRecord)
		r.Post("/records/{recordID}/attachments", s.addAttachments)
		r.Get("/attachments/{attachmentID}", s.attachment)
		r.Post("/attachments/{attachmentID}/delete", s.deleteAttachment)
		r.Post("/service-types", s.createServiceType)
	})
	return r
}

func (s *Server) base(r *http.Request, title string) (pageData, error) {
	v, e := s.store.ListVehicles(r.Context(), false)
	return pageData{Title: title, Authenticated: true, User: middleware.User(r), NavVehicles: v}, e
}
func (s *Server) render(w http.ResponseWriter, status int, name string, d pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if e := s.ui.Render(w, name, d); e != nil {
		slog.Error("render", "page", name, "error", e)
	}
}
func (s *Server) renderNamed(w http.ResponseWriter, page, name string, d pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if e := s.ui.RenderNamed(w, page, name, d); e != nil {
		s.fail(w, e)
	}
}
func (s *Server) fail(w http.ResponseWriter, e error) {
	slog.Error("request failed", "error", e)
	http.Error(w, "Something went wrong", http.StatusInternalServerError)
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	target, _ := safeRedirectTarget(r.URL.Query().Get("redirect"))
	d := pageData{Title: "Sign in", Development: s.cfg.AuthMode == config.AuthDevelopment, Error: loginErrorMessage(r.URL.Query().Get("error")), Redirect: target}
	s.render(w, http.StatusOK, "login", d)
}
func (s *Server) devLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AuthMode != config.AuthDevelopment || strings.EqualFold(s.cfg.AppEnv, "production") {
		http.NotFound(w, r)
		return
	}
	_, token, e := s.auth.DevLogin(r.Context())
	if e != nil {
		s.fail(w, e)
		return
	}
	middleware.SetSession(w, token, s.cfg.SecureCookies(), s.cfg.SessionTTL)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AuthMode != config.AuthGoogle || s.google == nil {
		http.NotFound(w, r)
		return
	}
	nonce, e := auth.State()
	if e != nil {
		s.fail(w, e)
		return
	}
	target, _ := safeRedirectTarget(r.URL.Query().Get("redirect"))
	state, e := signedOAuthState(nonce, target, s.cfg.GoogleClientSecret)
	if e != nil {
		s.fail(w, e)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oauthStateCookie, Value: state, Path: "/api/auth/google/callback", HttpOnly: true, Secure: s.cfg.SecureCookies(), SameSite: http.SameSiteLaxMode, MaxAge: 600})
	http.Redirect(w, r, s.google.AuthURL(state), http.StatusFound)
}
func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	cookie, e := r.Cookie(oauthStateCookie)
	state := r.URL.Query().Get("state")
	target, validState := parseOAuthState(state, s.cfg.GoogleClientSecret)
	if e != nil || cookie.Value == "" || !hmac.Equal([]byte(state), []byte(cookie.Value)) || !validState {
		http.Redirect(w, r, loginErrorLocation(loginErrorExpired), http.StatusSeeOther)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oauthStateCookie, Path: "/api/auth/google/callback", MaxAge: -1, HttpOnly: true, Secure: s.cfg.SecureCookies(), SameSite: http.SameSiteLaxMode})
	claims, e := s.google.Exchange(r.Context(), r.URL.Query().Get("code"))
	if e != nil {
		http.Redirect(w, r, loginErrorLocation(loginErrorOAuth), http.StatusSeeOther)
		return
	}
	if !claims.EmailVerified || !s.google.Allowed(claims.Email) {
		http.Redirect(w, r, loginErrorLocation(loginErrorNotInvited), http.StatusSeeOther)
		return
	}
	_, token, e := s.auth.Login(r.Context(), claims)
	if e != nil {
		s.fail(w, e)
		return
	}
	middleware.SetSession(w, token, s.cfg.SecureCookies(), s.cfg.SessionTTL)
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func loginErrorLocation(code string) string {
	return "/login?error=" + url.QueryEscape(code)
}

func loginErrorMessage(code string) string {
	switch code {
	case loginErrorExpired:
		return "Sign-in expired. Please try again."
	case loginErrorOAuth:
		return "Google sign-in could not be completed. Please try again."
	case loginErrorNotInvited:
		return "This verified Google account is not invited to Carma."
	default:
		return ""
	}
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie(middleware.CookieName); e == nil {
		if e = s.auth.Logout(r.Context(), c.Value); e != nil {
			s.fail(w, e)
			return
		}
	}
	middleware.ClearSession(w, s.cfg.SecureCookies())
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	d, e := s.base(r, "Garage")
	if e != nil {
		s.fail(w, e)
		return
	}
	d.Vehicles, e = s.store.ListVehicles(r.Context(), false)
	if e != nil {
		s.fail(w, e)
		return
	}
	rs, e := s.store.ListReminders(r.Context(), nil, false)
	if e != nil {
		s.fail(w, e)
		return
	}
	today := day(s.now())
	for _, x := range rs {
		result := reminder.Evaluate(x, today)
		if result.Status == reminder.Due || result.Status == reminder.Soon {
			d.Attention = append(d.Attention, result)
		}
	}
	s.render(w, http.StatusOK, "dashboard", d)
}
func (s *Server) archivedVehicles(w http.ResponseWriter, r *http.Request) {
	d, e := s.base(r, "Archived vehicles")
	if e != nil {
		s.fail(w, e)
		return
	}
	d.Archived = true
	d.Vehicles, e = s.store.ListVehicles(r.Context(), true)
	if e != nil {
		s.fail(w, e)
		return
	}
	s.render(w, http.StatusOK, "records", d)
}

func (s *Server) newVehicle(w http.ResponseWriter, r *http.Request) {
	d, e := s.base(r, "Add vehicle")
	if e != nil {
		s.fail(w, e)
		return
	}
	s.render(w, http.StatusOK, "vehicle-form", d)
}
func (s *Server) editVehicle(w http.ResponseWriter, r *http.Request) {
	v, e := s.getVehicle(r)
	if e != nil {
		s.notFound(w, e)
		return
	}
	d, e := s.base(r, "Edit "+v.Nickname)
	if e != nil {
		s.fail(w, e)
		return
	}
	d.Editing = true
	// Show the effective latest mileage in the editable field, including mileage
	// known only from historical records on vehicles created before this field.
	v.CurrentOdometer = v.LatestOdometer
	d.Vehicle = v
	s.render(w, http.StatusOK, "vehicle-form", d)
}
func (s *Server) createVehicle(w http.ResponseWriter, r *http.Request) { s.saveVehicle(w, r, false) }
func (s *Server) updateVehicle(w http.ResponseWriter, r *http.Request) { s.saveVehicle(w, r, true) }
func (s *Server) saveVehicle(w http.ResponseWriter, r *http.Request, editing bool) {
	if e := s.parseMultipart(w, r, 8<<20); e != nil {
		s.multipartError(w, e)
		return
	}
	var old model.Vehicle
	var e error
	if editing {
		old, e = s.getVehicle(r)
		if e != nil {
			s.notFound(w, e)
			return
		}
	}
	v, validation := vehicleFromForm(r)
	if editing {
		v.ID = old.ID
		v.PhotoKey = old.PhotoKey
		v.CreatedAt = old.CreatedAt
	} else {
		v.ID = uuid.New()
		v.CreatedAt = s.now()
	}
	v.UpdatedAt = s.now()
	var newKey string
	if f, h, e := r.FormFile("photo"); e == nil {
		defer f.Close()
		obj, se := s.assets.Save(r.Context(), f, s.cfg.MaxUploadBytes)
		if se != nil {
			validation = "Photo must be JPEG, PNG, WebP, or HEIC and within the upload limit."
		} else if !strings.HasPrefix(obj.ContentType, "image/") {
			_ = s.assets.Delete(r.Context(), obj.Key)
			validation = "Vehicle photo must be an image."
		} else {
			v.PhotoKey = obj.Key
			newKey = obj.Key
			_ = h
		}
	}
	if validation != "" {
		if newKey != "" {
			_ = s.assets.Delete(r.Context(), newKey)
		}
		if editing {
			v.PhotoKey = old.PhotoKey
		}
		d, _ := s.base(r, "Vehicle")
		d.Editing = editing
		d.Vehicle = v
		d.Error = validation
		s.render(w, 400, "vehicle-form", d)
		return
	}
	if editing {
		_, e = s.store.UpdateVehicle(r.Context(), v)
	} else {
		_, e = s.store.CreateVehicle(r.Context(), v)
	}
	if e != nil {
		if newKey != "" {
			_ = s.assets.Delete(r.Context(), newKey)
		}
		s.fail(w, e)
		return
	}
	if editing && newKey != "" && old.PhotoKey != "" {
		_ = s.assets.Delete(r.Context(), old.PhotoKey)
	}
	http.Redirect(w, r, "/vehicles/"+v.ID.String(), 303)
}
func vehicleFromForm(r *http.Request) (model.Vehicle, string) {
	v := model.Vehicle{Nickname: strings.TrimSpace(r.FormValue("nickname")), Make: strings.TrimSpace(r.FormValue("make")), Model: strings.TrimSpace(r.FormValue("model")), VIN: strings.TrimSpace(r.FormValue("vin")), LicensePlate: strings.TrimSpace(r.FormValue("license_plate")), Notes: strings.TrimSpace(r.FormValue("notes"))}
	if v.Nickname == "" {
		return v, "Nickname is required."
	}
	if raw := strings.TrimSpace(r.FormValue("year")); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1886 || n > 9999 {
			return v, "Year is invalid."
		}
		v.Year = &n
	}
	if raw := strings.TrimSpace(r.FormValue("current_odometer")); raw != "" {
		n, e := strconv.ParseInt(raw, 10, 64)
		if e != nil || n < 0 {
			return v, "Current odometer must be a nonnegative whole number."
		}
		v.CurrentOdometer = &n
	}
	return v, ""
}
func (s *Server) archiveVehicle(w http.ResponseWriter, r *http.Request) {
	id, e := uuid.Parse(chi.URLParam(r, "vehicleID"))
	if e != nil {
		http.NotFound(w, r)
		return
	}
	if e = s.store.ArchiveVehicle(r.Context(), id); e != nil {
		s.notFound(w, e)
		return
	}
	http.Redirect(w, r, "/", 303)
}
func (s *Server) vehiclePhoto(w http.ResponseWriter, r *http.Request) {
	v, e := s.getVehicle(r)
	if e != nil || v.PhotoKey == "" {
		http.NotFound(w, r)
		return
	}
	s.serveObject(w, r, v.PhotoKey, filepath.Base(v.PhotoKey), contentTypeForKey(v.PhotoKey))
}

func (s *Server) vehicle(w http.ResponseWriter, r *http.Request) {
	v, e := s.getVehicle(r)
	if e != nil {
		s.notFound(w, e)
		return
	}
	d, e := s.base(r, v.Nickname)
	if e != nil {
		s.fail(w, e)
		return
	}
	d.Vehicle = v
	d.Params = cleanParams(r.URL.Query())
	q := recordQuery(r, &v.ID)
	d.Records, e = s.store.ListRecords(r.Context(), q)
	if e != nil {
		s.fail(w, e)
		return
	}
	d.Types, e = s.store.ListServiceTypes(r.Context())
	if e != nil {
		s.fail(w, e)
		return
	}
	rs, e := s.store.ListReminders(r.Context(), &v.ID, false)
	if e != nil {
		s.fail(w, e)
		return
	}
	for _, x := range rs {
		d.Reminders = append(d.Reminders, reminder.Evaluate(x, day(s.now())))
	}
	if isHTMX(r) {
		s.renderNamed(w, "vehicle", "records-section", d)
		return
	}
	s.render(w, 200, "vehicle", d)
}
func (s *Server) allRecords(w http.ResponseWriter, r *http.Request) {
	d, e := s.base(r, "All records")
	if e != nil {
		s.fail(w, e)
		return
	}
	d.Params = cleanParams(r.URL.Query())
	d.Records, e = s.store.ListRecords(r.Context(), recordQuery(r, nil))
	if e != nil {
		s.fail(w, e)
		return
	}
	d.Types, e = s.store.ListServiceTypes(r.Context())
	if e != nil {
		s.fail(w, e)
		return
	}
	if isHTMX(r) {
		s.renderNamed(w, "records", "records-section", d)
		return
	}
	s.render(w, 200, "records", d)
}
func recordQuery(r *http.Request, vehicleID *uuid.UUID) model.RecordQuery {
	v := r.URL.Query()
	q := model.RecordQuery{VehicleID: vehicleID, Search: strings.TrimSpace(v.Get("q")), Sort: v.Get("sort"), Desc: v.Get("direction") != "asc"}
	if id, e := uuid.Parse(v.Get("type")); e == nil {
		q.ServiceTypeID = &id
	}
	if t, e := time.Parse("2006-01-02", v.Get("from")); e == nil {
		q.From = &t
	}
	if t, e := time.Parse("2006-01-02", v.Get("to")); e == nil {
		q.To = &t
	}
	return q
}
func cleanParams(in url.Values) url.Values {
	out := url.Values{}
	for _, k := range []string{"q", "type", "from", "to", "sort", "direction"} {
		if v := in.Get(k); v != "" {
			out.Set(k, v)
		}
	}
	return out
}

func (s *Server) newRecord(w http.ResponseWriter, r *http.Request) {
	v, e := s.getVehicle(r)
	if e != nil {
		s.notFound(w, e)
		return
	}
	d, e := s.recordFormData(r, v, model.Record{OccurredOn: day(s.now())}, false)
	if e != nil {
		s.fail(w, e)
		return
	}
	s.render(w, 200, "record-form", d)
}
func (s *Server) editRecord(w http.ResponseWriter, r *http.Request) {
	rec, _, e := s.getRecord(r)
	if e != nil {
		s.notFound(w, e)
		return
	}
	v, e := s.store.GetVehicle(r.Context(), rec.VehicleID)
	if e != nil {
		s.notFound(w, e)
		return
	}
	d, e := s.recordFormData(r, v, rec, true)
	if e != nil {
		s.fail(w, e)
		return
	}
	s.render(w, 200, "record-form", d)
}
func (s *Server) recordFormData(r *http.Request, v model.Vehicle, rec model.Record, editing bool) (pageData, error) {
	d, e := s.base(r, "Record")
	if e != nil {
		return d, e
	}
	d.Vehicle = v
	d.Record = rec
	d.Editing = editing
	d.Types, e = s.store.ListServiceTypes(r.Context())
	return d, e
}
func (s *Server) createRecord(w http.ResponseWriter, r *http.Request) {
	v, e := s.getVehicle(r)
	if e != nil {
		s.notFound(w, e)
		return
	}
	if e = s.parseMultipart(w, r, 16<<20); e != nil {
		s.multipartError(w, e)
		return
	}
	rec, msg := recordFromForm(r)
	rec.ID = uuid.New()
	rec.VehicleID = v.ID
	rec.CreatedBy = middleware.User(r).ID
	rec.CreatedAt = s.now()
	rec.UpdatedAt = rec.CreatedAt
	if msg != "" {
		s.recordFormError(w, r, v, rec, false, msg)
		return
	}
	as, keys, e := s.saveUploads(r, rec.ID, "receipts")
	if e != nil {
		s.cleanup(r, keys)
		s.recordFormError(w, r, v, rec, false, e.Error())
		return
	}
	if _, e = s.store.CreateRecord(r.Context(), rec, as); e != nil {
		s.cleanup(r, keys)
		s.fail(w, e)
		return
	}
	http.Redirect(w, r, "/records/"+rec.ID.String(), 303)
}
func (s *Server) updateRecord(w http.ResponseWriter, r *http.Request) {
	old, _, e := s.getRecord(r)
	if e != nil {
		s.notFound(w, e)
		return
	}
	if e = r.ParseForm(); e != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	rec, msg := recordFromForm(r)
	rec.ID = old.ID
	rec.VehicleID = old.VehicleID
	rec.CreatedBy = old.CreatedBy
	rec.CreatedAt = old.CreatedAt
	rec.UpdatedAt = s.now()
	v, e := s.store.GetVehicle(r.Context(), rec.VehicleID)
	if e != nil {
		s.notFound(w, e)
		return
	}
	if msg != "" {
		s.recordFormError(w, r, v, rec, true, msg)
		return
	}
	if _, e = s.store.UpdateRecord(r.Context(), rec); e != nil {
		s.fail(w, e)
		return
	}
	http.Redirect(w, r, "/records/"+rec.ID.String(), 303)
}
func recordFromForm(r *http.Request) (model.Record, string) {
	rec := model.Record{Vendor: strings.TrimSpace(r.FormValue("vendor")), Notes: strings.TrimSpace(r.FormValue("notes"))}
	var e error
	if rec.OccurredOn, e = time.Parse("2006-01-02", r.FormValue("occurred_on")); e != nil {
		return rec, "Date is required."
	}
	if rec.ServiceTypeID, e = uuid.Parse(r.FormValue("service_type_id")); e != nil {
		return rec, "Service type is required."
	}
	if raw := strings.TrimSpace(r.FormValue("odometer")); raw != "" {
		n, e := strconv.ParseInt(strings.ReplaceAll(raw, ",", ""), 10, 64)
		if e != nil || n < 0 {
			return rec, "Odometer must be a non-negative whole number."
		}
		rec.OdometerMiles = &n
	}
	if raw := strings.TrimSpace(r.FormValue("cost")); raw != "" {
		n, e := parseCents(raw)
		if e != nil {
			return rec, "Cost must be a non-negative amount with at most two decimals."
		}
		rec.CostCents = &n
	}
	return rec, ""
}
func parseCents(raw string) (int64, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.ReplaceAll(raw, ",", ""), "$"))
	parts := strings.Split(raw, ".")
	if len(parts) > 2 || len(parts[0]) == 0 {
		return 0, errors.New("bad cost")
	}
	if len(parts) == 2 && len(parts[1]) > 2 {
		return 0, errors.New("bad cost")
	}
	whole, e := strconv.ParseInt(parts[0], 10, 64)
	if e != nil || whole < 0 {
		return 0, errors.New("bad cost")
	}
	frac := int64(0)
	if len(parts) == 2 {
		f := parts[1]
		if len(f) == 1 {
			f += "0"
		}
		if f != "" {
			frac, e = strconv.ParseInt(f, 10, 64)
			if e != nil {
				return 0, e
			}
		}
	}
	if whole > (math.MaxInt64-frac)/100 {
		return 0, errors.New("bad cost")
	}
	return whole*100 + frac, nil
}
func (s *Server) recordFormError(w http.ResponseWriter, r *http.Request, v model.Vehicle, rec model.Record, editing bool, msg string) {
	d, e := s.recordFormData(r, v, rec, editing)
	if e != nil {
		s.fail(w, e)
		return
	}
	d.Error = msg
	s.render(w, 400, "record-form", d)
}
func (s *Server) record(w http.ResponseWriter, r *http.Request) {
	rec, as, e := s.getRecord(r)
	if e != nil {
		s.notFound(w, e)
		return
	}
	d, e := s.base(r, rec.ServiceTypeName)
	if e != nil {
		s.fail(w, e)
		return
	}
	d.Record = rec
	d.Attachments = as
	s.render(w, 200, "record", d)
}
func (s *Server) deleteRecord(w http.ResponseWriter, r *http.Request) {
	rec, _, e := s.getRecord(r)
	if e != nil {
		s.notFound(w, e)
		return
	}
	keys, e := s.store.DeleteRecord(r.Context(), rec.ID)
	if e != nil {
		s.notFound(w, e)
		return
	}
	s.cleanup(r, keys)
	http.Redirect(w, r, "/vehicles/"+rec.VehicleID.String(), 303)
}

func (s *Server) addAttachments(w http.ResponseWriter, r *http.Request) {
	rec, _, e := s.getRecord(r)
	if e != nil {
		s.notFound(w, e)
		return
	}
	if e = s.parseMultipart(w, r, 16<<20); e != nil {
		s.multipartError(w, e)
		return
	}
	as, keys, e := s.saveUploads(r, rec.ID, "receipts")
	if e != nil {
		s.cleanup(r, keys)
		http.Error(w, e.Error(), 400)
		return
	}
	if len(as) == 0 {
		http.Error(w, "Choose at least one receipt", 400)
		return
	}
	if e = s.store.AddAttachments(r.Context(), rec.ID, as); e != nil {
		s.cleanup(r, keys)
		s.fail(w, e)
		return
	}
	http.Redirect(w, r, "/records/"+rec.ID.String(), 303)
}
func (s *Server) saveUploads(r *http.Request, rid uuid.UUID, field string) ([]model.Attachment, []string, error) {
	if r.MultipartForm == nil {
		return nil, nil, nil
	}
	var out []model.Attachment
	var keys []string
	for _, h := range r.MultipartForm.File[field] {
		f, e := h.Open()
		if e != nil {
			return out, keys, e
		}
		obj, e := s.assets.Save(r.Context(), f, s.cfg.MaxUploadBytes)
		_ = f.Close()
		if e != nil {
			return out, keys, fmt.Errorf("%s: %w", safeFilename(h.Filename), e)
		}
		keys = append(keys, obj.Key)
		out = append(out, model.Attachment{ID: uuid.New(), RecordID: rid, OriginalFilename: safeFilename(h.Filename), ContentType: obj.ContentType, ByteSize: obj.Size, StorageKey: obj.Key, CreatedAt: s.now()})
	}
	return out, keys, nil
}
func (s *Server) attachment(w http.ResponseWriter, r *http.Request) {
	_, as, e := s.findAttachment(r)
	if e != nil {
		s.notFound(w, e)
		return
	}
	s.serveObject(w, r, as.StorageKey, as.OriginalFilename, as.ContentType)
}
func (s *Server) deleteAttachment(w http.ResponseWriter, r *http.Request) {
	rec, a, e := s.findAttachment(r)
	if e != nil {
		s.notFound(w, e)
		return
	}
	if e = s.assets.Delete(r.Context(), a.StorageKey); e != nil {
		s.fail(w, e)
		return
	}
	_, e = s.store.DeleteAttachment(r.Context(), a.ID)
	if e != nil {
		s.notFound(w, e)
		return
	}
	http.Redirect(w, r, "/records/"+rec.ID.String(), 303)
}
func (s *Server) findAttachment(r *http.Request) (model.Record, model.Attachment, error) {
	id, e := uuid.Parse(chi.URLParam(r, "attachmentID"))
	if e != nil {
		return model.Record{}, model.Attachment{}, repository.ErrNotFound
	}
	return s.store.GetAttachment(r.Context(), id)
}
func (s *Server) serveObject(w http.ResponseWriter, r *http.Request, key, name, contentType string) {
	f, e := s.assets.Open(r.Context(), key)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	end, e := f.Seek(0, io.SeekEnd)
	if e != nil {
		s.fail(w, e)
		return
	}
	_, _ = f.Seek(0, io.SeekStart)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": safeFilename(name)}))
	http.ServeContent(w, r, name, time.Time{}, f)
	_ = end
}
func (s *Server) cleanup(r *http.Request, keys []string) {
	for _, k := range keys {
		if e := s.assets.Delete(r.Context(), k); e != nil {
			slog.Warn("asset cleanup failed", "key", k, "error", e)
		}
	}
}

func (s *Server) remindersPage(w http.ResponseWriter, r *http.Request) {
	v, e := s.getVehicle(r)
	if e != nil {
		s.notFound(w, e)
		return
	}
	d, e := s.base(r, "Reminders")
	if e != nil {
		s.fail(w, e)
		return
	}
	d.Vehicle = v
	d.Types, e = s.store.ListServiceTypes(r.Context())
	if e != nil {
		s.fail(w, e)
		return
	}
	rs, e := s.store.ListReminders(r.Context(), &v.ID, true)
	if e != nil {
		s.fail(w, e)
		return
	}
	for _, x := range rs {
		d.Reminders = append(d.Reminders, reminder.Evaluate(x, day(s.now())))
	}
	s.render(w, 200, "reminders", d)
}
func (s *Server) upsertReminder(w http.ResponseWriter, r *http.Request) {
	v, e := s.getVehicle(r)
	if e != nil {
		s.notFound(w, e)
		return
	}
	if e = r.ParseForm(); e != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	stid, e := uuid.Parse(r.FormValue("service_type_id"))
	if e != nil {
		http.Error(w, "Service type is required", 400)
		return
	}
	months, me := optionalPositiveInt(r.FormValue("months"))
	miles, mie := optionalPositiveInt64(r.FormValue("miles"))
	if me != nil || mie != nil || (months == nil && miles == nil) {
		http.Error(w, "Set a positive month or mileage interval", 400)
		return
	}
	now := s.now()
	rm := model.Reminder{ID: uuid.New(), VehicleID: v.ID, ServiceTypeID: stid, IntervalMonths: months, IntervalMiles: miles, StartingOdometer: v.LatestOdometer, Enabled: r.FormValue("enabled") == "true", CreatedAt: now, UpdatedAt: now}
	if _, e = s.store.UpsertReminder(r.Context(), rm); e != nil {
		s.fail(w, e)
		return
	}
	if isHTMX(r) {
		s.renderReminderList(w, r, v)
		return
	}
	http.Redirect(w, r, "/vehicles/"+v.ID.String()+"/reminders", 303)
}

func (s *Server) deleteReminder(w http.ResponseWriter, r *http.Request) {
	v, e := s.getVehicle(r)
	if e != nil {
		s.notFound(w, e)
		return
	}
	id, e := uuid.Parse(chi.URLParam(r, "reminderID"))
	if e != nil {
		http.NotFound(w, r)
		return
	}
	rows, e := s.store.ListReminders(r.Context(), &v.ID, true)
	if e != nil {
		s.fail(w, e)
		return
	}
	found := false
	for _, row := range rows {
		if row.ID == id {
			found = true
			break
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	if e = s.store.DeleteReminder(r.Context(), id); e != nil {
		s.notFound(w, e)
		return
	}
	if isHTMX(r) {
		s.renderReminderList(w, r, v)
		return
	}
	http.Redirect(w, r, "/vehicles/"+v.ID.String()+"/reminders", http.StatusSeeOther)
}

func (s *Server) renderReminderList(w http.ResponseWriter, r *http.Request, v model.Vehicle) {
	d, e := s.base(r, "Reminders")
	if e != nil {
		s.fail(w, e)
		return
	}
	d.Vehicle = v
	d.Types, e = s.store.ListServiceTypes(r.Context())
	if e != nil {
		s.fail(w, e)
		return
	}
	rows, e := s.store.ListReminders(r.Context(), &v.ID, true)
	if e != nil {
		s.fail(w, e)
		return
	}
	for _, row := range rows {
		d.Reminders = append(d.Reminders, reminder.Evaluate(row, day(s.now())))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if e = s.ui.RenderNamed(w, "reminders", "reminder-list", d); e != nil {
		s.fail(w, e)
	}
}

func isHTMX(r *http.Request) bool { return strings.EqualFold(r.Header.Get("HX-Request"), "true") }

func (s *Server) parseMultipart(w http.ResponseWriter, r *http.Request, memory int64) error {
	limit := s.cfg.MaxMultipartBytes
	if limit <= 0 {
		limit = 128 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	return r.ParseMultipartForm(memory)
}

func (s *Server) multipartError(w http.ResponseWriter, e error) {
	var tooLarge *http.MaxBytesError
	if errors.As(e, &tooLarge) || strings.Contains(strings.ToLower(e.Error()), "request body too large") {
		http.Error(w, "Multipart request exceeds the configured total limit", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, "Invalid multipart form", http.StatusBadRequest)
}
func optionalPositiveInt(raw string) (*int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	n, e := strconv.Atoi(raw)
	if e != nil || n <= 0 {
		return nil, errors.New("positive")
	}
	return &n, nil
}
func optionalPositiveInt64(raw string) (*int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	n, e := strconv.ParseInt(raw, 10, 64)
	if e != nil || n <= 0 {
		return nil, errors.New("positive")
	}
	return &n, nil
}
func (s *Server) createServiceType(w http.ResponseWriter, r *http.Request) {
	if e := r.ParseForm(); e != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 100 {
		http.Error(w, "Type name is required and must be under 100 characters", 400)
		return
	}
	_, e := s.store.CreateServiceType(r.Context(), model.ServiceType{ID: uuid.New(), Name: name, CreatedAt: s.now()})
	if errors.Is(e, repository.ErrConflict) {
		http.Error(w, "A service type with that name already exists", 409)
		return
	}
	if e != nil {
		s.fail(w, e)
		return
	}
	dest, _ := safeRedirectTarget(r.FormValue("return_to"))
	http.Redirect(w, r, dest, 303)
}

func (s *Server) vehicleCSV(w http.ResponseWriter, r *http.Request) {
	v, e := s.getVehicle(r)
	if e != nil {
		s.notFound(w, e)
		return
	}
	s.writeCSV(w, r, recordQuery(r, &v.ID), "carma-"+slug(v.Nickname)+"-"+s.now().Format("2006-01-02")+".csv")
}
func (s *Server) globalCSV(w http.ResponseWriter, r *http.Request) {
	s.writeCSV(w, r, recordQuery(r, nil), "carma-all-records-"+s.now().Format("2006-01-02")+".csv")
}
func (s *Server) writeCSV(w http.ResponseWriter, r *http.Request, q model.RecordQuery, name string) {
	rows, e := s.store.ListRecords(r.Context(), q)
	if e != nil {
		s.fail(w, e)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	if e = carmaexport.CSV(w, rows); e != nil {
		slog.Error("csv export", "error", e)
	}
}

func (s *Server) getVehicle(r *http.Request) (model.Vehicle, error) {
	id, e := uuid.Parse(chi.URLParam(r, "vehicleID"))
	if e != nil {
		return model.Vehicle{}, repository.ErrNotFound
	}
	return s.store.GetVehicle(r.Context(), id)
}
func (s *Server) getRecord(r *http.Request) (model.Record, []model.Attachment, error) {
	id, e := uuid.Parse(chi.URLParam(r, "recordID"))
	if e != nil {
		return model.Record{}, nil, repository.ErrNotFound
	}
	return s.store.GetRecord(r.Context(), id)
}
func (s *Server) notFound(w http.ResponseWriter, e error) {
	if errors.Is(e, repository.ErrNotFound) {
		http.Error(w, "Not found", 404)
	} else {
		s.fail(w, e)
	}
}
func day(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}
func safeFilename(v string) string {
	v = filepath.Base(strings.ReplaceAll(v, "\\", "/"))
	v = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, v)
	if strings.TrimSpace(v) == "" {
		return "receipt"
	}
	return v
}
func contentTypeForKey(k string) string {
	switch strings.ToLower(filepath.Ext(k)) {
	case ".jpg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".heic":
		return "image/heic"
	default:
		return "application/octet-stream"
	}
}

type oauthStatePayload struct {
	Nonce    string `json:"n"`
	Redirect string `json:"r"`
}

func signedOAuthState(nonce, redirect, secret string) (string, error) {
	payload, e := json.Marshal(oauthStatePayload{Nonce: nonce, Redirect: redirect})
	if e != nil {
		return "", e
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func parseOAuthState(state, secret string) (string, bool) {
	parts := strings.Split(state, ".")
	if len(parts) != 2 {
		return "/", false
	}
	signature, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return "/", false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return "/", false
	}
	payloadBytes, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return "/", false
	}
	var payload oauthStatePayload
	if e = json.Unmarshal(payloadBytes, &payload); e != nil || payload.Nonce == "" {
		return "/", false
	}
	target, ok := safeRedirectTarget(payload.Redirect)
	if !ok {
		return "/", false
	}
	return target, true
}

func safeRedirectTarget(raw string) (string, bool) {
	if raw == "" {
		return "/", true
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.Contains(raw, "\\") {
		return "/", false
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return "/", false
		}
	}
	u, e := url.ParseRequestURI(raw)
	if e != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") || strings.Contains(u.Path, "\\") {
		return "/", false
	}
	decoded, e := url.PathUnescape(u.EscapedPath())
	if e != nil || strings.Contains(decoded, "\\") || strings.HasPrefix(decoded, "//") {
		return "/", false
	}
	return raw, true
}

func slug(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	var b strings.Builder
	dash := false
	for _, r := range v {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
