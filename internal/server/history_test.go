package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

func TestVehicleHistoryListsRecordsOldestFirstWithTotals(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	types, _ := f.store.ListServiceTypes(t.Context())
	for _, fields := range []map[string]string{
		{"occurred_on": "2026-05-02", "odometer": "61000", "cost": "450.50", "vendor": "Tire Barn", "notes": "four new tires"},
		{"occurred_on": "2025-11-14", "odometer": "55000", "cost": "89.00", "vendor": "Jiffy Lube", "notes": "<b>synthetic</b>"},
		{"occurred_on": "2026-01-20", "vendor": "Driveway"},
	} {
		fields["service_type_id"] = types[0].ID.String()
		filename, file := "", []byte(nil)
		if fields["vendor"] == "Jiffy Lube" {
			filename, file = "receipt.pdf", []byte("%PDF-1.7\nreceipt")
		}
		b, ct := formBody(t, fields, filename, file)
		if w := f.do(t, "POST", "/vehicles/"+v.ID.String()+"/records", b, ct); w.Code != 303 {
			t.Fatalf("record: %d %s", w.Code, w.Body.String())
		}
	}

	w := f.do(t, "GET", "/vehicles/"+v.ID.String()+"/history", nil, "")
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, body)
	}
	for _, want := range []string{"2019 Subaru Outback", "Prepared Jul 31, 2026", "Total spent", "$539.50", "Receipts on file</dt><dd>1<", "Service records</dt><dd>3 · Nov 14, 2025 – May 2, 2026", "data-print-history", "&lt;b&gt;synthetic&lt;/b&gt;"} {
		if !strings.Contains(body, want) {
			t.Fatalf("history missing %q: %s", want, body)
		}
	}
	first, second, third := strings.Index(body, "Jiffy Lube"), strings.Index(body, "Driveway"), strings.Index(body, "Tire Barn")
	if first < 0 || !(first < second && second < third) {
		t.Fatalf("records not oldest first: %d %d %d", first, second, third)
	}

	vehiclePage := f.do(t, "GET", "/vehicles/"+v.ID.String(), nil, "")
	if !strings.Contains(vehiclePage.Body.String(), `href="/vehicles/`+v.ID.String()+`/history/print"`) {
		t.Fatal("vehicle page does not link to the printable history")
	}
	if missing := f.do(t, "GET", "/vehicles/"+uuid.NewString()+"/history", nil, ""); missing.Code != http.StatusNotFound {
		t.Fatalf("unknown vehicle status=%d", missing.Code)
	}
}

func TestVehicleHistoryPrintRendersCostsShownAndHidden(t *testing.T) {
	f := setup(t)
	b, ct := formBody(t, map[string]string{
		"nickname": "Dan's Outback", "year": "2019", "make": "Subaru", "model": "Outback",
		"vin": "4S4BSANC5K3245678", "current_odometer": "54800",
	}, "", nil)
	if w := f.do(t, "POST", "/vehicles", b, ct); w.Code != http.StatusSeeOther {
		t.Fatalf("create vehicle: %d %s", w.Code, w.Body.String())
	}
	vehicles, err := f.store.ListVehicles(t.Context(), false)
	if err != nil || len(vehicles) != 1 {
		t.Fatalf("vehicles=%v err=%v", vehicles, err)
	}
	v := vehicles[0]
	types, err := f.store.ListServiceTypes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	typeID := map[string]string{}
	for _, serviceType := range types {
		typeID[serviceType.Name] = serviceType.ID.String()
	}
	for _, fields := range []map[string]string{
		{"occurred_on": "2026-05-02", "odometer": "61000", "cost": "450.50", "vendor": "Tire Barn", "notes": "four new tires", "service_type_id": typeID["Repair"]},
		{"occurred_on": "2025-11-14", "odometer": "55000", "cost": "89.00", "vendor": "Jiffy Lube", "notes": "<b>synthetic</b>", "service_type_id": typeID["Oil change"]},
		{"occurred_on": "2026-01-20", "vendor": "Driveway", "notes": "rotated in the driveway", "service_type_id": typeID["Tire rotation"]},
	} {
		filename, file := "", []byte(nil)
		if fields["vendor"] == "Jiffy Lube" {
			filename, file = "receipt.pdf", []byte("%PDF-1.7\nreceipt")
		}
		body, contentType := formBody(t, fields, filename, file)
		if w := f.do(t, "POST", "/vehicles/"+v.ID.String()+"/records", body, contentType); w.Code != http.StatusSeeOther {
			t.Fatalf("record: %d %s", w.Code, w.Body.String())
		}
	}
	reported := time.Date(2019, 6, 27, 0, 0, 0, 0, time.UTC)
	if _, err = f.store.SaveRecallCheck(t.Context(), v, []model.Recall{
		{CampaignNumber: "19V493000", Component: "AIR BAGS", Summary: "The inflator may rupture.", Consequence: "Injury.", Remedy: "Replace the inflator.", ReportReceivedOn: &reported, ParkIt: true},
		{CampaignNumber: "20V123000", Component: "SEATS", Summary: "Already fixed at the dealer."},
	}, f.s.now()); err != nil {
		t.Fatal(err)
	}
	dismissed := f.s.now()
	if err = f.store.SetRecallDismissed(t.Context(), v.ID, "20V123000", &dismissed); err != nil {
		t.Fatal(err)
	}

	path := "/vehicles/" + v.ID.String() + "/history/print"
	shown := f.do(t, "GET", path, nil, "")
	hidden := f.do(t, "GET", path+"?costs=hidden", nil, "")
	if shown.Code != http.StatusOK || hidden.Code != http.StatusOK {
		t.Fatalf("shown=%d hidden=%d", shown.Code, hidden.Code)
	}
	shownBody, hiddenBody := shown.Body.String(), hidden.Body.String()
	for _, body := range []string{shownBody, hiddenBody} {
		for _, want := range []string{
			"2019 Subaru Outback", "4S4BSANC5K3245678", "61000 mi", "Prepared Jul 31, 2026",
			"Service records</dt><dd>3 · Nov 14, 2025 – May 2, 2026", "Receipts on file</dt><dd>1<",
			"Jiffy Lube", "Driveway", "Tire Barn", "55000", "rotated in the driveway",
			"&lt;b&gt;synthetic&lt;/b&gt;", "data-label=\"Mileage\">—",
			"AIR BAGS", "Campaign 19V493000", "reported Jun 27, 2019", "Do not drive this vehicle until the recall repair is done.",
			"The inflator may rupture.", "Replace the inflator.",
			`class="history-toolbar history-print-toolbar no-print"`, `class="topbar"`, "Hide costs", "data-print-history",
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("print history missing %q:\n%s", want, body)
			}
		}
		if strings.Contains(body, "20V123000") || strings.Contains(body, "Already fixed") || strings.Contains(body, "SEATS") {
			t.Fatalf("dismissed recall rendered:\n%s", body)
		}
		first, second, third := strings.Index(body, "Jiffy Lube"), strings.Index(body, "Driveway"), strings.Index(body, "Tire Barn")
		if first < 0 || !(first < second && second < third) {
			t.Fatalf("records not oldest first: %d %d %d", first, second, third)
		}
	}
	for _, want := range []string{"Total spent", "$539.50", "$89.00", "$450.50", `>Cost</th>`, `data-label="Cost">—`, `data-history-costs>`} {
		if !strings.Contains(shownBody, want) {
			t.Fatalf("costs shown missing %q:\n%s", want, shownBody)
		}
	}
	if strings.Contains(shownBody, "data-history-costs checked") {
		t.Fatal("hide-costs toggle checked while costs are shown")
	}
	for _, absent := range []string{"Total spent", "$", `>Cost</th>`, `data-label="Cost"`} {
		if strings.Contains(hiddenBody, absent) {
			t.Fatalf("costs hidden still has %q:\n%s", absent, hiddenBody)
		}
	}
	if !strings.Contains(hiddenBody, "data-history-costs checked") {
		t.Fatal("hide-costs toggle unchecked while costs are hidden")
	}
	other := f.do(t, "GET", path+"?costs=show", nil, "")
	if other.Code != http.StatusOK || !strings.Contains(other.Body.String(), "Total spent") {
		t.Fatalf("unknown costs query should show prices: %d", other.Code)
	}
	if missing := f.do(t, "GET", "/vehicles/"+uuid.NewString()+"/history/print", nil, ""); missing.Code != http.StatusNotFound {
		t.Fatalf("unknown vehicle status=%d", missing.Code)
	}
}

func TestVehicleHistoryPrintWithoutRecords(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	w := f.do(t, "GET", "/vehicles/"+v.ID.String()+"/history/print?costs=hidden", nil, "")
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "No service records yet.") || !strings.Contains(body, "No open recalls on file.") || strings.Contains(body, "Total spent") || strings.Contains(body, "$") {
		t.Fatalf("status=%d body=%s", w.Code, body)
	}
	w = f.do(t, "GET", "/vehicles/"+v.ID.String()+"/history/print", nil, "")
	body = w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "Total spent</dt><dd>—") || !strings.Contains(body, "VIN</dt><dd>—") {
		t.Fatalf("status=%d body=%s", w.Code, body)
	}
}

func TestVehicleHistoryWithoutRecords(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	w := f.do(t, "GET", "/vehicles/"+v.ID.String()+"/history", nil, "")
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "No service records yet.") || !strings.Contains(body, "Total spent</dt><dd>—") {
		t.Fatalf("status=%d body=%s", w.Code, body)
	}
}
