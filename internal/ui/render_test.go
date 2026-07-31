package ui

import (
	"bytes"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/reminder"
	"github.com/google/uuid"
)

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
		Params        url.Values
		Types         []model.ServiceType
		Records       []model.Record
		Reminders     []any
	}
	v := model.Vehicle{ID: uuid.New(), Nickname: "Outback"}
	var b bytes.Buffer
	err = r.Render(&b, "vehicle", data{Authenticated: true, User: &model.User{}, Vehicle: v, Params: url.Values{}, Types: []model.ServiceType{{ID: uuid.New(), Name: "Oil change"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "Export CSV") || !strings.Contains(b.String(), "Manage reminders") {
		t.Fatal("vehicle page rendered incompletely")
	}
	for _, name := range []string{"Search records", "Filter by service type", "From date", "To date", "Sort records by", "Sort direction"} {
		if !strings.Contains(b.String(), `aria-label="`+name+`"`) {
			t.Fatalf("missing accessible name %q", name)
		}
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
