package server

import (
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/bitofbytes-io/carma/internal/assets"
	"github.com/bitofbytes-io/carma/internal/auth"
	"github.com/bitofbytes-io/carma/internal/config"
	carmaexport "github.com/bitofbytes-io/carma/internal/export"
	"github.com/bitofbytes-io/carma/internal/metrics"
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
	static *staticAssets
	now    func() time.Time
	// readyTimeout bounds /ready's dependency checks, which run one at a time.
	readyTimeout        time.Duration
	dbCheck, assetCheck singleCheck
	// vinDecoder is nil until EnableVINDecode, which cmd/carma always calls.
	vinDecoder VINDecoder
	// metrics is nil until EnableMetrics; a nil *metrics.Metrics records nothing.
	metrics *metrics.Metrics
}

// EnableMetrics records request, sign-in, and upload metrics into m. Call it
// before Router.
func (s *Server) EnableMetrics(m *metrics.Metrics) {
	s.metrics = m
}

func New(cfg *config.Config, store repository.Store, assetStore assets.Store, authService *auth.Service, google auth.Google) (*Server, error) {
	static, err := newStaticAssets(os.DirFS(staticDir))
	if err != nil {
		return nil, err
	}
	renderer, err := ui.NewWithAssets(static.URL)
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
		static: static,
		now:    time.Now,

		readyTimeout: readyTimeout,
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
	OdometerForm                              ui.OdometerForm
	ReminderPresets                           []ui.ReminderPreset
	// Recalls are the vehicle page's NHTSA campaigns, open and dismissed;
	// RecallAlerts are the dashboard's vehicles with open ones.
	Recalls, DismissedRecalls []model.Recall
	RecallAlerts              []ui.RecallAlert
	// VINDecode shows the vehicle form's "Decode VIN" button; VINMessage is
	// the outcome of a decode.
	VINDecode  bool
	VINMessage string
	VINError   bool
	// History totals the printable history's records, dated Generated.
	History   carmaexport.HistorySummary
	Generated time.Time
	// Spend is the vehicle page's cost summary for SpendYear and all time.
	Spend     model.SpendSummary
	SpendYear int
}

func (s *Server) Router() http.Handler {
	router := chi.NewRouter()
	// Metrics wraps Recoverer so recovered panics are counted as 500s.
	router.Use(middleware.SecurityHeaders, chimw.RequestID, chimw.RealIP, s.metrics.Middleware, chimw.Recoverer, middleware.SameOrigin)
	router.Handle("/static/*", s.static)
	router.Get("/health", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("ok"))
	})
	router.Get("/ready", s.ready)
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
		router.Post("/vehicles/vin-decode", s.decodeVIN)
		router.Post("/vehicles", s.createVehicle)
		router.Get("/vehicles/{vehicleID}", s.vehicle)
		router.Get("/vehicles/{vehicleID}/edit", s.editVehicle)
		router.Post("/vehicles/{vehicleID}", s.updateVehicle)
		router.Post("/vehicles/{vehicleID}/archive", s.archiveVehicle)
		router.Post("/vehicles/{vehicleID}/odometer", s.addOdometerReading)
		router.Get("/vehicles/{vehicleID}/photo", s.vehiclePhoto)
		router.Get("/vehicles/{vehicleID}/records/new", s.newRecord)
		router.Post("/vehicles/{vehicleID}/records", s.createRecord)
		router.Get("/vehicles/{vehicleID}/export.csv", s.vehicleCSV)
		router.Get("/vehicles/{vehicleID}/history", s.vehicleHistory)
		router.Get("/vehicles/{vehicleID}/reminders", s.remindersPage)
		router.Post("/vehicles/{vehicleID}/reminders", s.upsertReminder)
		router.Post("/vehicles/{vehicleID}/reminders/{reminderID}/delete", s.deleteReminder)
		router.Post("/vehicles/{vehicleID}/recalls/{campaign}/dismiss", s.dismissRecall)
		router.Post("/vehicles/{vehicleID}/recalls/{campaign}/restore", s.restoreRecall)
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
