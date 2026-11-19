package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/reminder"
	"github.com/google/uuid"
)

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
		RecallsCheckedAt          *time.Time
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
	if err = r.RenderNamed(&b, "vehicle", "recalls-section", section); err != nil {
		t.Fatal(err)
	}
	if body = b.String(); !strings.Contains(body, `id="recalls"`) || !strings.Contains(body, "Safety recalls") || !strings.Contains(body, "Not checked yet") ||
		strings.Contains(body, "No open recalls") || strings.Contains(body, "Last checked") || !strings.Contains(body, "shortly after a vehicle is saved") {
		t.Fatalf("a vehicle never checked should say so: %s", body)
	}
	b.Reset()
	section.Vehicle.Model = ""
	if err = r.RenderNamed(&b, "vehicle", "recalls-section", section); err != nil || !strings.Contains(b.String(), "Not checked yet") || !strings.Contains(b.String(), "Add a year, make, and model") {
		t.Fatalf("a vehicle that cannot be checked should say what is missing: %q err=%v", b.String(), err)
	}
	b.Reset()
	checked := time.Date(2026, time.November, 13, 15, 4, 0, 0, time.UTC)
	section.Vehicle.Model = "Outback"
	section.RecallsCheckedAt = &checked
	if err = r.RenderNamed(&b, "vehicle", "recalls-section", section); err != nil {
		t.Fatal(err)
	}
	if body = b.String(); !strings.Contains(body, "No open recalls") || !strings.Contains(body, "Last checked Nov 13, 2026.") || strings.Contains(body, "Not checked yet") || strings.Contains(body, `class="recall"`) {
		t.Fatalf("a checked vehicle without recalls should say none are open: %s", body)
	}
	b.Reset()
	section.Recalls = []model.Recall{{VehicleID: vehicleID, CampaignNumber: "19V493000", Component: "STRUCTURE"}}
	if err = r.RenderNamed(&b, "vehicle", "recalls-section", section); err != nil {
		t.Fatal(err)
	}
	if body = b.String(); !strings.Contains(body, "Campaign 19V493000") || !strings.Contains(body, "Last checked Nov 13, 2026.") || strings.Contains(body, "Not checked yet") {
		t.Fatalf("a checked vehicle with recalls should list them and when it was checked: %s", body)
	}
}
