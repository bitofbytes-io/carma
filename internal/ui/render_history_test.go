package ui

import (
	"bytes"
	"net/url"
	"strings"
	"testing"
	"time"

	carmaexport "github.com/bitofbytes-io/carma/internal/export"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

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
		Title            string
		Flash            string
		Authenticated    bool
		User             *model.User
		NavVehicles      []model.Vehicle
		Vehicle          model.Vehicle
		Records          []model.Record
		Recalls          []model.Recall
		RecallsCheckedAt *time.Time
		History          carmaexport.HistorySummary
		Generated        time.Time
		HideCosts        bool
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
		if first < 0 || first >= second || second >= third {
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

func TestWarrantyBadgeAndHistoryColumn(t *testing.T) {
	renderer, err := New()
	if err != nil {
		t.Fatal(err)
	}
	until := time.Date(2028, 3, 3, 0, 0, 0, 0, time.UTC)
	limit := int64(36000)
	active := model.Record{ID: uuid.New(), OccurredOn: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), ServiceTypeName: "Battery", WarrantyUntilOn: &until, WarrantyUntilMiles: &limit}
	expired := model.Record{ID: uuid.New(), OccurredOn: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), ServiceTypeName: "Brakes", WarrantyUntilMiles: &limit}
	none := model.Record{ID: uuid.New(), OccurredOn: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC), ServiceTypeName: "Wipers"}
	records := []model.Record{expired, none, active}
	vehicle := model.Vehicle{ID: uuid.New(), Nickname: "Outback"}
	type data struct {
		Title, Error, Flash string
		Authenticated       bool
		User                *model.User
		NavVehicles         []model.Vehicle
		Vehicle             model.Vehicle
		RecordsURL          string
		Params              url.Values
		Types               []model.ServiceType
		Records             []model.Record
		Reminders           []any
		Recalls             []model.Recall
		DismissedRecalls    []model.Recall
		RecallsCheckedAt    *time.Time
		Spend               model.SpendSummary
		SpendYear           int
		History             carmaexport.HistorySummary
		Generated           time.Time
		WarrantyActive      map[uuid.UUID]bool
		Record              model.Record
		HideCosts, Editing  bool
		LogAgainFrom        string
		Vendors             []string
		Attachments         []model.Attachment
	}
	page := data{
		Authenticated: true, User: &model.User{}, Vehicle: vehicle, RecordsURL: "/vehicles/" + vehicle.ID.String(),
		Params: url.Values{}, Records: records, History: carmaexport.Summarize(records),
		Generated: time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC), WarrantyActive: map[uuid.UUID]bool{active.ID: true},
	}

	var body bytes.Buffer
	if err := renderer.Render(&body, "vehicle", page); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(body.String(), "Under warranty until"); got != 1 || !strings.Contains(body.String(), "Under warranty until Mar 3, 2028 or 36,000 mi") {
		t.Fatalf("vehicle list badges=%d: %s", got, body.String())
	}

	for _, template := range []string{"history-print", "history"} {
		body.Reset()
		if err := renderer.Render(&body, template, page); err != nil {
			t.Fatalf("%s: %v", template, err)
		}
		html := body.String()
		for _, want := range []string{">Warranty</th>", "Until Mar 3, 2028 or 36,000 mi", `<span class="muted">Active</span>`, "Until 36,000 mi", `<span class="muted">Expired</span>`} {
			if !strings.Contains(html, want) {
				t.Fatalf("%s missing %q:\n%s", template, want, html)
			}
		}
	}

	for _, tc := range []struct {
		name   string
		record model.Record
		active bool
		want   string
	}{
		{name: "active", record: active, active: true, want: "Under warranty until Mar 3, 2028 or 36,000 mi"},
		{name: "expired", record: expired, want: "Expired · was until 36,000 mi"},
		{name: "none", record: none},
	} {
		body.Reset()
		page.Record = tc.record
		page.WarrantyActive = map[uuid.UUID]bool{tc.record.ID: tc.active}
		if err := renderer.Render(&body, "record", page); err != nil {
			t.Fatal(err)
		}
		html := body.String()
		if tc.want == "" {
			if strings.Contains(html, "<dt>Warranty</dt>") {
				t.Fatalf("%s: record without warranty shows a warranty row", tc.name)
			}
			continue
		}
		if !strings.Contains(html, tc.want) {
			t.Fatalf("%s missing %q:\n%s", tc.name, tc.want, html)
		}
		if got := strings.Contains(html, `class="warranty-badge"`); got != tc.active {
			t.Fatalf("%s badge present=%v want=%v", tc.name, got, tc.active)
		}
	}

	body.Reset()
	page.Record = model.Record{ID: uuid.New(), OccurredOn: until, WarrantyUntilOn: &until, WarrantyUntilMiles: &limit}
	if err := renderer.Render(&body, "record-form", page); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`name="warranty_until" value="2028-03-03"`, `name="warranty_miles" value="36000"`} {
		if !strings.Contains(body.String(), want) {
			t.Fatalf("record form missing %q:\n%s", want, body.String())
		}
	}
}
