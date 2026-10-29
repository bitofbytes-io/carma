package ui

import (
	"bytes"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	carmaexport "github.com/bitofbytes-io/carma/internal/export"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/reminder"
	"github.com/google/uuid"
)

func TestDashboardOdometerFormSitsOutsideCardLink(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	miles := int64(62410)
	vehicle := model.Vehicle{ID: uuid.New(), Nickname: "Wagon", LatestOdometer: &miles}
	type data struct {
		Title, Error, Flash string
		Authenticated       bool
		User                *model.User
		NavVehicles         []model.Vehicle
		Vehicles            []model.Vehicle
		Attention           []reminder.Result
		OdometerForm        OdometerForm

		RecallAlerts []RecallAlert
	}
	var b bytes.Buffer
	if err := r.Render(&b, "dashboard", data{Authenticated: true, User: &model.User{}, Vehicles: []model.Vehicle{vehicle}}); err != nil {
		t.Fatal(err)
	}
	body := b.String()
	card := body[strings.Index(body, `id="vehicle-card-`+vehicle.ID.String()+`"`):]
	link, form := strings.Index(card, `<a class="vehicle-link"`), strings.Index(card, `<form class="odometer-form"`)
	if link < 0 || form < 0 || !strings.Contains(card[link:form], "</a>") {
		t.Fatalf("mileage form is not after the card link: %s", card)
	}
	for _, want := range []string{"62410 mi", `name="return" value="dashboard"`, `aria-label="Current mileage for Wagon"`, `id="attention"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard missing %s", want)
		}
	}

	b.Reset()
	update := data{OdometerForm: OdometerForm{Vehicle: vehicle, Return: "dashboard"}}
	if err := r.RenderNamed(&b, "dashboard", "odometer-update", update); err != nil {
		t.Fatal(err)
	}
	if body = b.String(); !strings.Contains(body, `id="vehicle-card-`+vehicle.ID.String()+`"`) || !strings.Contains(body, `<div hx-swap-oob="innerHTML:#attention">`) || strings.Contains(body, "<html") {
		t.Fatalf("odometer update partial: %s", body)
	}
}

func TestBaseTemplateDeclaresSupportedColorSchemes(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	type data struct {
		Title         string
		Authenticated bool
		Development   bool
		Error         string
		Flash         string
		Redirect      string
	}
	var b bytes.Buffer
	if err := r.Render(&b, "login", data{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `<meta name="color-scheme" content="light dark">`) {
		t.Fatal("base template does not advertise light and dark color schemes")
	}
}

func TestBaseTemplateLinksStaticFilesThroughAssetURLs(t *testing.T) {
	r, err := NewWithAssets(func(name string) string { return "/static/" + name + "?v=test" })
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	type data struct {
		Title, Error, Flash, Redirect string
		Authenticated, Development    bool
	}
	if err := r.Render(&b, "login", data{}); err != nil {
		t.Fatal(err)
	}
	body := b.String()
	for _, name := range []string{"favicon.svg", "styles.css", "reminders.css", "htmx.min.js", "app.js"} {
		if !strings.Contains(body, `="/static/`+name+`?v=test"`) {
			t.Errorf("base template does not link %s by its asset URL: %s", name, body)
		}
		if _, err := os.Stat(filepath.Join("..", "..", "static", name)); err != nil {
			t.Errorf("linked static file: %v", err)
		}
	}
	if links := strings.Count(body, `"/static/`); links != 5 {
		t.Errorf("base template links %d static files, want 5 versioned links", links)
	}
}

func TestLoginTemplateRendersAuthenticationVariantsWithoutInviteOnlyFooter(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	type data struct {
		Title         string
		Authenticated bool
		Development   bool
		Error         string
		Flash         string
		Redirect      string
	}
	tests := []struct {
		name      string
		data      data
		want      []string
		doNotWant []string
	}{
		{
			name: "Google sign-in",
			data: data{
				Error:    "This Google account is not authorized for Carma.",
				Redirect: "/vehicles/123?sort=cost&direction=desc",
			},
			want: []string{
				`<div class="error" role="alert">This Google account is not authorized for Carma.</div>`,
				`<a class="primary wide center" href="/api/auth/google?redirect=%2Fvehicles%2F123%3Fsort%3Dcost%26direction%3Ddesc">Sign in with Google</a>`,
			},
			doNotWant: []string{
				`<form method="post" action="/login">`,
				"Continue as local developer",
			},
		},
		{
			name: "development login",
			data: data{Development: true},
			want: []string{
				`<form method="post" action="/login"><button class="primary wide">Continue as local developer</button></form>`,
			},
			doNotWant: []string{
				"Sign in with Google",
				`/api/auth/google`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			if err := r.Render(&b, "login", tt.data); err != nil {
				t.Fatal(err)
			}
			body := b.String()
			for _, want := range tt.want {
				if !strings.Contains(body, want) {
					t.Errorf("missing login markup %q in %s", want, body)
				}
			}
			for _, unwanted := range append(tt.doNotWant, "Access is invite-only.", "<small>") {
				if strings.Contains(body, unwanted) {
					t.Errorf("unexpected login markup %q in %s", unwanted, body)
				}
			}
		})
	}
}

func TestVehicleFormShowsExistingPhotoOnlyWhenEditingSavedPhoto(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	vehicleID := uuid.New()
	tests := []struct {
		name         string
		editing      bool
		vehicle      model.Vehicle
		wantPreview  bool
		wantOptional bool
	}{
		{
			name:         "edit with saved photo",
			editing:      true,
			vehicle:      model.Vehicle{ID: vehicleID, Nickname: "Outback", PhotoKey: "saved/photo.jpg"},
			wantPreview:  true,
			wantOptional: false,
		},
		{
			name:         "edit without saved photo",
			editing:      true,
			vehicle:      model.Vehicle{ID: vehicleID, Nickname: "Outback"},
			wantPreview:  false,
			wantOptional: true,
		},
		{
			name:         "create ignores stale photo data",
			vehicle:      model.Vehicle{ID: vehicleID, Nickname: "Outback", PhotoKey: "stale/photo.jpg"},
			wantPreview:  false,
			wantOptional: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			type data struct {
				Title, Error, Flash string
				Authenticated       bool
				Editing             bool
				Vehicle             model.Vehicle
				ReminderPresets     []ReminderPreset

				VINDecode  bool
				VINMessage string
				VINError   bool
			}
			var b bytes.Buffer
			if err := r.Render(&b, "vehicle-form", data{Editing: tt.editing, Vehicle: tt.vehicle}); err != nil {
				t.Fatal(err)
			}
			body := b.String()
			for _, want := range []string{
				`<label for="vehicle-photo">Photo</label>`,
				`id="vehicle-photo" type="file" name="photo"`,
				`aria-describedby="vehicle-photo-help"`,
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("missing photo input markup %q in %s", want, body)
				}
			}
			inputStart := strings.Index(body, `id="vehicle-photo"`)
			if inputStart < 0 {
				t.Fatalf("photo input not found in %s", body)
			}
			inputEnd := strings.Index(body[inputStart:], ">")
			if inputEnd < 0 {
				t.Fatalf("photo input is incomplete in %s", body)
			}
			if input := body[inputStart : inputStart+inputEnd]; strings.Contains(input, "value=") {
				t.Fatalf("file input was populated: %s", input)
			}

			previewMarkers := []string{
				`class="existing-photo"`,
				`src="/vehicles/` + vehicleID.String() + `/photo"`,
				"Current photo",
				"This photo is already saved. Choose a new file to replace it.",
			}
			for _, marker := range previewMarkers {
				if got := strings.Contains(body, marker); got != tt.wantPreview {
					t.Fatalf("preview marker %q present=%v, want %v in %s", marker, got, tt.wantPreview, body)
				}
			}
			if got := strings.Contains(body, "Optional. Add a photo of your vehicle."); got != tt.wantOptional {
				t.Fatalf("optional guidance present=%v, want %v in %s", got, tt.wantOptional, body)
			}
		})
	}
}

func TestVehicleFormOffersReminderPresetsOnlyWhenAdding(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	type data struct {
		Title, Error, Flash string
		Authenticated       bool
		Editing             bool
		Vehicle             model.Vehicle
		ReminderPresets     []ReminderPreset

		VINDecode  bool
		VINMessage string
		VINError   bool
	}
	presets := ReminderPresets([]string{"oil change"})
	var b bytes.Buffer
	if err := r.Render(&b, "vehicle-form", data{ReminderPresets: presets}); err != nil {
		t.Fatal(err)
	}
	body := b.String()
	for _, want := range []string{
		"Starter reminders",
		`name="reminder_preset" value="Oil change" checked>`,
		`name="reminder_preset" value="Tire rotation" >`,
		"every 6 months or 5,000 mi",
		"every 7,500 mi",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("add form missing %q in %s", want, body)
		}
	}

	b.Reset()
	if err := r.Render(&b, "vehicle-form", data{Editing: true, Vehicle: model.Vehicle{ID: uuid.New(), Nickname: "Wagon"}, ReminderPresets: presets}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "reminder_preset") {
		t.Fatalf("edit form offers reminder presets: %s", b.String())
	}
}

func TestVehicleTemplateRendersThroughExportControls(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	type data struct {
		Title         string
		Error         string
		Flash         string
		Authenticated bool
		User          *model.User
		NavVehicles   []model.Vehicle
		Vehicle       model.Vehicle
		RecordsURL    string
		Params        url.Values
		Types         []model.ServiceType
		Records       []model.Record
		Reminders     []any

		Recalls, DismissedRecalls []model.Recall
		Spend                     model.SpendSummary
		SpendYear                 int
	}
	v := model.Vehicle{ID: uuid.New(), Nickname: "Outback"}
	var b bytes.Buffer
	err = r.Render(&b, "vehicle", data{Authenticated: true, User: &model.User{}, Vehicle: v, RecordsURL: "/vehicles/" + v.ID.String(), Params: url.Values{}, Types: []model.ServiceType{{ID: uuid.New(), Name: "Oil change"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `href="/vehicles/`+v.ID.String()+`/export.csv?"`) || !strings.Contains(b.String(), "Manage reminders") {
		t.Fatal("vehicle page rendered incompletely")
	}
	for _, name := range []string{"Search records", "Filter by service type", "From date", "To date", "Sort records by", "Sort direction"} {
		if !strings.Contains(b.String(), `aria-label="`+name+`"`) {
			t.Fatalf("missing accessible name %q", name)
		}
	}
}

func TestVehicleTemplateRendersReminderIntervals(t *testing.T) {
	months6 := 6
	months12 := 12
	var miles10000 int64 = 10000
	var miles30000 int64 = 30000
	tests := []struct {
		name   string
		months *int
		miles  *int64
		want   string
	}{
		{name: "months and mileage", months: &months6, miles: &miles10000, want: "6 mo / 10000 mi"},
		{name: "mileage only", miles: &miles30000, want: "30000 mi"},
		{name: "months only", months: &months12, want: "12 mo"},
		{name: "neither", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := New()
			if err != nil {
				t.Fatal(err)
			}
			type data struct {
				Title         string
				Error         string
				Flash         string
				Authenticated bool
				User          *model.User
				NavVehicles   []model.Vehicle
				Vehicle       model.Vehicle
				RecordsURL    string
				Params        url.Values
				Types         []model.ServiceType
				Records       []model.Record
				Reminders     []reminder.Result

				Recalls, DismissedRecalls []model.Recall
				Spend                     model.SpendSummary
				SpendYear                 int
			}
			vehicle := model.Vehicle{ID: uuid.New(), Nickname: "Outback"}
			row := reminder.Result{
				Reminder: model.Reminder{
					ID:              uuid.New(),
					VehicleID:       vehicle.ID,
					ServiceTypeID:   uuid.New(),
					ServiceTypeName: "Oil change",
					IntervalMonths:  tt.months,
					IntervalMiles:   tt.miles,
					Enabled:         true,
				},
				Status: reminder.OK,
			}
			var b bytes.Buffer
			if err := r.Render(&b, "vehicle", data{Authenticated: true, User: &model.User{}, Vehicle: vehicle, Params: url.Values{}, Reminders: []reminder.Result{row}}); err != nil {
				t.Fatal(err)
			}
			rendered := b.String()
			if want := "<small>" + tt.want + "</small>"; !strings.Contains(rendered, want) {
				t.Fatalf("missing reminder interval %q in %s", want, rendered)
			}
		})
	}
}

func TestRemindersTemplateUsesLabeledResponsiveStructure(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	vehicle := model.Vehicle{ID: uuid.New(), Nickname: "Ranger"}
	serviceType := model.ServiceType{ID: uuid.New(), Name: "Oil change"}
	reminderID := uuid.New()
	months := 6
	row := reminder.Result{Reminder: model.Reminder{ID: reminderID, VehicleID: vehicle.ID, ServiceTypeID: serviceType.ID, ServiceTypeName: serviceType.Name, IntervalMonths: &months, Enabled: true, CreatedAt: time.Now()}, Status: reminder.OK}
	type data struct {
		Title         string
		Error         string
		Flash         string
		Authenticated bool
		User          *model.User
		NavVehicles   []model.Vehicle
		Vehicle       model.Vehicle
		Types         []model.ServiceType
		Reminders     []reminder.Result
	}
	var b bytes.Buffer
	if err := r.Render(&b, "reminders", data{Authenticated: true, User: &model.User{}, Vehicle: vehicle, Types: []model.ServiceType{serviceType}, Reminders: []reminder.Result{row}}); err != nil {
		t.Fatal(err)
	}
	body := b.String()
	for _, want := range []string{
		`class="reminder-row reminder-entry"`,
		`<div class="reminder-row header" aria-hidden="true">
      <div class="reminder-header-fields"><span>Service type</span><span>Every (months)</span><span>Every (miles)</span><span class="reminder-header-status">Status</span><span class="reminder-header-enabled">Enabled</span></div>
      <span>Actions</span>
    </div>`,
		`class="reminder-field reminder-enabled"`,
		`class="reminder-actions"`,
		`form="reminder-` + reminderID.String() + `"`,
		`<span class="field-label">Service type</span>`,
		`<span class="field-label">Every (months)</span>`,
		`<span class="field-label">Every (miles)</span>`,
		`aria-label="Enable Oil change reminder"`,
		`<label><span>Service type</span><select name="service_type_id"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing responsive/accessibility structure %q in %s", want, body)
		}
	}
}

func TestGroupRecallsCountsPerVehicle(t *testing.T) {
	outback, civic := uuid.New(), uuid.New()
	alerts := GroupRecalls([]model.Recall{
		{VehicleID: outback, VehicleName: "Outback", CampaignNumber: "1"},
		{VehicleID: civic, VehicleName: "Civic", CampaignNumber: "2"},
		{VehicleID: outback, VehicleName: "Outback", CampaignNumber: "3", ParkIt: true},
	})
	if len(alerts) != 2 || alerts[0].VehicleID != outback || alerts[0].Count != 2 || !alerts[0].ParkIt || alerts[1].Count != 1 || alerts[1].ParkIt {
		t.Fatalf("alerts = %+v", alerts)
	}
}

func TestRecallTemplates(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	vehicleID := uuid.New()
	var b bytes.Buffer
	attention := struct {
		Attention    []reminder.Result
		RecallAlerts []RecallAlert
	}{RecallAlerts: []RecallAlert{{VehicleID: vehicleID, VehicleName: "Outback", Count: 2, ParkIt: true}}}
	if err = r.RenderNamed(&b, "dashboard", "attention-body", attention); err != nil {
		t.Fatal(err)
	}
	if body := b.String(); !strings.Contains(body, `href="/vehicles/`+vehicleID.String()+`#recalls"`) || !strings.Contains(body, "RECALL · DO NOT DRIVE") || !strings.Contains(body, "2 open safety recalls") || strings.Contains(body, "Nothing needs attention") {
		t.Fatalf("attention: %s", body)
	}

	year := 2019
	received := time.Date(2019, time.June, 26, 0, 0, 0, 0, time.UTC)
	section := struct {
		Vehicle                   model.Vehicle
		Recalls, DismissedRecalls []model.Recall
		Spend                     model.SpendSummary
		SpendYear                 int
	}{
		Vehicle:          model.Vehicle{ID: vehicleID, Year: &year, Make: "Subaru", Model: "Outback"},
		Recalls:          []model.Recall{{VehicleID: vehicleID, CampaignNumber: "19V493000", Component: "STRUCTURE", Summary: "Spot welds <b>", Remedy: "Repair.", ReportReceivedOn: &received, ParkIt: true}},
		DismissedRecalls: []model.Recall{{VehicleID: vehicleID, CampaignNumber: "21V587000", Component: "FUEL PUMP"}},
	}
	b.Reset()
	if err = r.RenderNamed(&b, "vehicle", "recalls-section", section); err != nil {
		t.Fatal(err)
	}
	body := b.String()
	for _, want := range []string{`id="recalls"`, "2019 Subaru Outback", "Campaign 19V493000 · reported Jun 26, 2019", "Do not drive", "Spot welds &lt;b&gt;",
		`action="/vehicles/` + vehicleID.String() + `/recalls/19V493000/dismiss"`, "https://www.nhtsa.gov/recalls?nhtsaId=19V493000",
		"Dismissed (1)", `action="/vehicles/` + vehicleID.String() + `/recalls/21V587000/restore"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("recalls section missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "vymm=") || !strings.Contains(body, `href="https://www.nhtsa.gov/recalls" target="_blank"`) {
		t.Fatalf("recalls section without a VIN should link to the plain NHTSA lookup: %s", body)
	}
	for _, invalid := range []string{"UNKNOWN", "1FTER4KP4TLE1726", "1FTER4KP4TLE1726O"} {
		b.Reset()
		section.Vehicle.VIN = invalid
		if err = r.RenderNamed(&b, "vehicle", "recalls-section", section); err != nil {
			t.Fatal(err)
		}
		if body = b.String(); strings.Contains(body, "vymm=") || strings.Contains(body, "Check this VIN") || !strings.Contains(body, `href="https://www.nhtsa.gov/recalls" target="_blank"`) {
			t.Fatalf("invalid VIN %q should fall back to the plain NHTSA lookup: %s", invalid, body)
		}
	}
	b.Reset()
	section.Vehicle.VIN = "1fter-4kp4tle17265"
	if err = r.RenderNamed(&b, "vehicle", "recalls-section", section); err != nil {
		t.Fatal(err)
	}
	if body = b.String(); strings.Count(body, `href="https://www.nhtsa.gov/recalls?vymm=1FTER4KP4TLE17265"`) != 2 || !strings.Contains(body, "look up this VIN on nhtsa.gov") || !strings.Contains(body, "Check this VIN ›") {
		t.Fatalf("recalls section should deep link the VIN in the intro and on each open recall: %s", body)
	}
	b.Reset()
	section.Recalls, section.DismissedRecalls = nil, nil
	if err = r.RenderNamed(&b, "vehicle", "recalls-section", section); err != nil || strings.TrimSpace(b.String()) != "" {
		t.Fatalf("no recalls should render nothing: %q err=%v", b.String(), err)
	}
}

func TestVehicleIdentityOffersVINDecodeOnlyWhenEnabled(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	type identity struct {
		Vehicle             model.Vehicle
		VINDecode, VINError bool
		VINMessage          string
	}
	var b bytes.Buffer
	if err = r.RenderNamed(&b, "vehicle-form", "vehicle-identity", identity{Vehicle: model.Vehicle{VIN: "1HGCM82633A004352"}}); err != nil {
		t.Fatal(err)
	}
	if body := b.String(); strings.Contains(body, "Decode VIN") || !strings.Contains(body, `value="1HGCM82633A004352"`) {
		t.Fatalf("decode disabled: %s", body)
	}
	b.Reset()
	if err = r.RenderNamed(&b, "vehicle-form", "vehicle-identity", identity{VINDecode: true, VINError: true, VINMessage: "NHTSA could not decode this VIN."}); err != nil {
		t.Fatal(err)
	}
	body := b.String()
	for _, want := range []string{`id="vehicle-identity"`, `hx-post="/vehicles/vin-decode"`, `hx-target="#vehicle-identity"`, `type="button"`, `class="vin-message full vin-error"`, "could not decode"} {
		if !strings.Contains(body, want) {
			t.Fatalf("decode enabled missing %q: %s", want, body)
		}
	}
}

func TestHistoryPrintRendersCostsShownAndHidden(t *testing.T) {
	renderer, err := New()
	if err != nil {
		t.Fatal(err)
	}
	year := 2019
	miles := int64(61000)
	earlier := int64(55000)
	oil := int64(8900)
	tires := int64(45050)
	reported := time.Date(2019, 6, 27, 0, 0, 0, 0, time.UTC)
	records := []model.Record{
		{OccurredOn: time.Date(2025, 11, 14, 0, 0, 0, 0, time.UTC), ServiceTypeName: "Oil change", OdometerMiles: &earlier, CostCents: &oil, Vendor: "Jiffy Lube", Notes: "<b>synthetic</b>", AttachmentCount: 1},
		{OccurredOn: time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC), ServiceTypeName: "Tire rotation", Vendor: "Driveway", Notes: "rotated in the driveway\nfront tires only"},
		{OccurredOn: time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC), ServiceTypeName: "Repair", OdometerMiles: &miles, CostCents: &tires, Vendor: "Tire Barn", Notes: "four new tires"},
	}
	type data struct {
		Title         string
		Flash         string
		Authenticated bool
		User          *model.User
		NavVehicles   []model.Vehicle
		Vehicle       model.Vehicle
		Records       []model.Record
		Recalls       []model.Recall
		History       carmaexport.HistorySummary
		Generated     time.Time
		HideCosts     bool
	}
	page := data{
		Authenticated: true,
		User:          &model.User{DisplayName: "Dan"},
		Vehicle:       model.Vehicle{ID: uuid.New(), Nickname: "Dan's Outback", Year: &year, Make: "Subaru", Model: "Outback", VIN: "4S4BSANC5K3245678", LatestOdometer: &miles},
		Records:       records,
		Recalls:       []model.Recall{{CampaignNumber: "19V493000", Component: "AIR BAGS", Summary: "The inflator may rupture.", ReportReceivedOn: &reported, ParkIt: true}},
		History:       carmaexport.Summarize(records),
		Generated:     time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC),
	}
	for _, hideCosts := range []bool{false, true} {
		page.HideCosts = hideCosts
		var body bytes.Buffer
		if err := renderer.Render(&body, "history-print", page); err != nil {
			t.Fatal(err)
		}
		html := body.String()
		for _, want := range []string{
			"2019 Subaru Outback", "4S4BSANC5K3245678", "61000 mi", "Prepared Jul 31, 2026",
			"Jiffy Lube", "Driveway", "Tire Barn", "rotated in the driveway\nfront tires only", "data-label=\"Mileage\">—",
			"&lt;b&gt;synthetic&lt;/b&gt;", "AIR BAGS", "Campaign 19V493000",
			"Do not drive this vehicle until the recall repair is done.",
			`class="history-toolbar history-print-toolbar no-print"`, `class="topbar"`,
		} {
			if !strings.Contains(html, want) {
				t.Fatalf("hideCosts=%v missing %q:\n%s", hideCosts, want, html)
			}
		}
		first, second, third := strings.Index(html, "Jiffy Lube"), strings.Index(html, "Driveway"), strings.Index(html, "Tire Barn")
		if !(first >= 0 && first < second && second < third) {
			t.Fatalf("hideCosts=%v records not oldest first", hideCosts)
		}
		if hideCosts {
			for _, absent := range []string{"Total spent", "$", ">Cost</th>", `data-label="Cost"`} {
				if strings.Contains(html, absent) {
					t.Fatalf("hideCosts=true still has %q:\n%s", absent, html)
				}
			}
			if !strings.Contains(html, "data-history-costs checked") {
				t.Fatal("hide-costs control is unchecked")
			}
			continue
		}
		for _, want := range []string{"Total spent", "$539.50", "$89.00", "$450.50", ">Cost</th>", `data-label="Cost">—`, "data-history-costs>"} {
			if !strings.Contains(html, want) {
				t.Fatalf("hideCosts=false missing %q:\n%s", want, html)
			}
		}
		if strings.Contains(html, "data-history-costs checked") {
			t.Fatal("hide-costs control is checked while costs are shown")
		}
	}
}

func TestVehicleTemplateLinksToHistoryPrint(t *testing.T) {
	renderer, err := New()
	if err != nil {
		t.Fatal(err)
	}
	type data struct {
		Title            string
		Error            string
		Flash            string
		Authenticated    bool
		User             *model.User
		NavVehicles      []model.Vehicle
		Vehicle          model.Vehicle
		RecordsURL       string
		Params           url.Values
		Types            []model.ServiceType
		Records          []model.Record
		Reminders        []any
		Recalls          []model.Recall
		DismissedRecalls []model.Recall
		Spend            model.SpendSummary
		SpendYear        int
	}
	vehicle := model.Vehicle{ID: uuid.New(), Nickname: "Outback"}
	var body bytes.Buffer
	if err := renderer.Render(&body, "vehicle", data{Authenticated: true, User: &model.User{}, Vehicle: vehicle, RecordsURL: "/vehicles/" + vehicle.ID.String(), Params: url.Values{}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.String(), `href="/vehicles/`+vehicle.ID.String()+`/history/print"`) {
		t.Fatalf("vehicle page does not link to the print view: %s", body.String())
	}
}

func TestDollarsFormatsCentsExactly(t *testing.T) {
	for cents, want := range map[int64]string{
		0:                "$0.00",
		5:                "$0.05",
		123450:           "$1234.50",
		-5:               "-$0.05",
		9007199254740993: "$90071992547409.93",
		math.MaxInt64:    "$92233720368547758.07",
		math.MinInt64:    "-$92233720368547758.08",
	} {
		if got := Dollars(cents); got != want {
			t.Errorf("Dollars(%d)=%q want %q", cents, got, want)
		}
	}
}
