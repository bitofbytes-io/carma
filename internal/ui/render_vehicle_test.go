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
		RecallsCheckedAt          *time.Time
		Spend                     model.SpendSummary
		SpendYear                 int
	}
	v := model.Vehicle{ID: uuid.New(), Nickname: "Outback"}
	var b bytes.Buffer
	err = r.Render(&b, "vehicle", data{Authenticated: true, User: &model.User{}, Vehicle: v, RecordsURL: "/vehicles/" + v.ID.String(), Params: url.Values{}, Types: []model.ServiceType{{ID: uuid.New(), Name: "Oil change"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `href="/vehicles/`+v.ID.String()+`/export.csv?"`) || !strings.Contains(b.String(), `href="/vehicles/`+v.ID.String()+`/export.xlsx?"`) || !strings.Contains(b.String(), "Manage reminders") {
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
				RecallsCheckedAt          *time.Time
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
		RecallsCheckedAt *time.Time
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
