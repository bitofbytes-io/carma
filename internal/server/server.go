package server

import (
	"net/http"
	"net/url"
	"time"

	"github.com/bitofbytes-io/carma/internal/assets"
	"github.com/bitofbytes-io/carma/internal/auth"
	"github.com/bitofbytes-io/carma/internal/config"
	"github.com/bitofbytes-io/carma/internal/middleware"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/reminder"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/bitofbytes-io/carma/internal/ui"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
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
	renderer, err := ui.New()
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg:    cfg,
		store:  store,
		assets: assetStore,
		auth:   authService,
		google: google,
		ui:     renderer,
		now:    time.Now,
	}, nil
}

type pageData struct {
	Title, Error, Flash, Redirect, RecordsURL string
	Authenticated, Development, Editing       bool
	User                                      *model.User
	NavVehicles, Vehicles                     []model.Vehicle
	Vehicle                                   model.Vehicle
	Record                                    model.Record
	Records                                   []model.Record
	Types                                     []model.ServiceType
	Attachments                               []model.Attachment
	Reminders, Attention                      []reminder.Result
	Params                                    url.Values
}

func (s *Server) Router() http.Handler {
	router := chi.NewRouter()
	router.Use(chimw.RequestID, chimw.RealIP, chimw.Recoverer, middleware.SameOrigin)
	router.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	router.Get("/health", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("ok"))
	})
	router.Get("/login", s.loginPage)
	router.Post("/login", s.devLogin)
	router.Get("/api/auth/google", s.oauthStart)
	router.Get("/api/auth/google/callback", s.oauthCallback)
	router.Group(func(router chi.Router) {
		router.Use(middleware.RequireAuth(s.auth, s.cfg.SecureCookies()))
		router.Post("/logout", s.logout)
		router.Get("/", s.dashboard)
		router.Get("/records", s.allRecords)
		router.Get("/records/export.csv", s.globalCSV)
		router.Get("/vehicles/archived", s.archivedVehicles)
		router.Get("/vehicles/new", s.newVehicle)
		router.Post("/vehicles", s.createVehicle)
		router.Get("/vehicles/{vehicleID}", s.vehicle)
		router.Get("/vehicles/{vehicleID}/edit", s.editVehicle)
		router.Post("/vehicles/{vehicleID}", s.updateVehicle)
		router.Post("/vehicles/{vehicleID}/archive", s.archiveVehicle)
		router.Get("/vehicles/{vehicleID}/photo", s.vehiclePhoto)
		router.Get("/vehicles/{vehicleID}/records/new", s.newRecord)
		router.Post("/vehicles/{vehicleID}/records", s.createRecord)
		router.Get("/vehicles/{vehicleID}/export.csv", s.vehicleCSV)
		router.Get("/vehicles/{vehicleID}/reminders", s.remindersPage)
		router.Post("/vehicles/{vehicleID}/reminders", s.upsertReminder)
		router.Post("/vehicles/{vehicleID}/reminders/{reminderID}/delete", s.deleteReminder)
		router.Get("/records/{recordID}", s.record)
		router.Get("/records/{recordID}/edit", s.editRecord)
		router.Post("/records/{recordID}", s.updateRecord)
		router.Post("/records/{recordID}/delete", s.deleteRecord)
		router.Post("/records/{recordID}/attachments", s.addAttachments)
		router.Get("/attachments/{attachmentID}", s.attachment)
		router.Post("/attachments/{attachmentID}/delete", s.deleteAttachment)
		router.Post("/service-types", s.createServiceType)
	})
	return router
}
