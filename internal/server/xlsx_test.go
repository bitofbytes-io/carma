package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"
)

func TestXLSXDownloadReadsBackTypedServiceHistory(t *testing.T) {
	f := setup(t)
	outback := createVehicle(t, f)
	year := 2021
	now := f.s.now()
	tacoma := model.Vehicle{ID: uuid.New(), Nickname: "Tacoma", Year: &year, Make: "Toyota", Model: "Tacoma", CreatedAt: now, UpdatedAt: now}
	if _, err := f.store.CreateVehicle(t.Context(), tacoma, nil); err != nil {
		t.Fatal(err)
	}
	types, err := f.store.ListServiceTypes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	oil, tires := types[0], types[0]
	for _, serviceType := range types {
		switch serviceType.Name {
		case "Oil change":
			oil = serviceType
		case "Tire rotation":
			tires = serviceType
		}
	}
	miles, later, oilCost, tireCost := int64(55000), int64(61000), int64(8900), int64(45050)
	for _, record := range []model.Record{
		{ID: uuid.New(), VehicleID: outback.ID, ServiceTypeID: oil.ID, CreatedBy: f.user.ID, OccurredOn: time.Date(2025, 11, 14, 0, 0, 0, 0, time.UTC), OdometerMiles: &miles, CostCents: &oilCost, Vendor: "Jiffy Lube", CreatedAt: now, UpdatedAt: now},
		{ID: uuid.New(), VehicleID: outback.ID, ServiceTypeID: tires.ID, CreatedBy: f.user.ID, OccurredOn: time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC), Vendor: "Driveway", CreatedAt: now, UpdatedAt: now},
		{ID: uuid.New(), VehicleID: tacoma.ID, ServiceTypeID: tires.ID, CreatedBy: f.user.ID, OccurredOn: time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC), OdometerMiles: &later, CostCents: &tireCost, Vendor: "Tire Barn", CreatedAt: now, UpdatedAt: now},
	} {
		if _, err = f.store.CreateRecord(t.Context(), record, nil); err != nil {
			t.Fatal(err)
		}
	}

	page := f.do(t, http.MethodGet, "/vehicles/"+outback.ID.String(), nil, "")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `/export.csv?`) || !strings.Contains(page.Body.String(), `/export.xlsx?`) {
		t.Fatalf("vehicle page export links: %d", page.Code)
	}

	response := f.do(t, http.MethodGet, "/vehicles/"+outback.ID.String()+"/export.xlsx?direction=desc&sort=cost", nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Fatalf("content type=%q", got)
	}
	if !strings.Contains(response.Header().Get("Content-Disposition"), "carma-dan-s-outback-2026-07-31.xlsx") {
		t.Fatalf("disposition=%q", response.Header().Get("Content-Disposition"))
	}
	book := openXLSX(t, response.Body.Bytes())
	if sheets := book.GetSheetList(); len(sheets) != 1 || sheets[0] != "Dan's Outback" {
		t.Fatalf("sheets=%v", sheets)
	}
	rows, err := book.GetRows("Dan's Outback")
	if err != nil || len(rows) != 4 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if rows[1][2] != "Oil change" || rows[2][2] != "Tire rotation" || rows[3][0] != "Total" {
		t.Fatalf("order=%v", rows)
	}
	assertXLSXDate(t, book, "Dan's Outback", "A2", "2025-11-14")
	assertXLSXRaw(t, book, "Dan's Outback", "E2", "89.00")
	assertXLSXRaw(t, book, "Dan's Outback", "E4", "89.00")

	filtered := f.do(t, http.MethodGet, "/vehicles/"+outback.ID.String()+"/export.xlsx?q=Jiffy", nil, "")
	filteredBook := openXLSX(t, filtered.Body.Bytes())
	filteredRows, err := filteredBook.GetRows("Dan's Outback")
	if err != nil || len(filteredRows) != 3 || filteredRows[1][3] != "Jiffy Lube" {
		t.Fatalf("filtered=%v err=%v", filteredRows, err)
	}
	assertXLSXRaw(t, filteredBook, "Dan's Outback", "E3", "89.00")

	global := f.do(t, http.MethodGet, "/records/export.xlsx", nil, "")
	if !strings.Contains(global.Header().Get("Content-Disposition"), "carma-all-records-2026-07-31.xlsx") {
		t.Fatalf("global disposition=%q", global.Header().Get("Content-Disposition"))
	}
	all := openXLSX(t, global.Body.Bytes())
	if sheets := all.GetSheetList(); len(sheets) != 2 || sheets[0] != "Dan's Outback" || sheets[1] != "Tacoma" {
		t.Fatalf("global sheets=%v", sheets)
	}
	assertXLSXDate(t, all, "Tacoma", "A2", "2026-05-02")
	assertXLSXRaw(t, all, "Tacoma", "E2", "450.50")
	assertXLSXRaw(t, all, "Tacoma", "E3", "450.50")

	unauth := httptest.NewRecorder()
	f.router.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "http://example.com/records/export.xlsx", nil))
	if unauth.Code != http.StatusSeeOther {
		t.Fatalf("unauthenticated status=%d", unauth.Code)
	}
}

func openXLSX(t *testing.T, body []byte) *excelize.File {
	t.Helper()
	book, err := excelize.OpenReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = book.Close() })
	return book
}

func assertXLSXDate(t *testing.T, book *excelize.File, sheet, cell, want string) {
	t.Helper()
	raw, err := book.GetCellValue(sheet, cell, excelize.Options{RawCellValue: true})
	if err != nil {
		t.Fatal(err)
	}
	serial, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		t.Fatalf("%s raw=%q err=%v", cell, raw, err)
	}
	got, err := excelize.ExcelDateToTime(serial, false)
	if err != nil || got.Format("2006-01-02") != want {
		t.Fatalf("%s date=%v raw=%s err=%v want=%s", cell, got, raw, err, want)
	}
	styleID, err := book.GetCellStyle(sheet, cell)
	if err != nil {
		t.Fatal(err)
	}
	style, err := book.GetStyle(styleID)
	if err != nil || style.CustomNumFmt == nil || *style.CustomNumFmt != "yyyy-mm-dd" {
		t.Fatalf("%s format=%v err=%v", cell, style, err)
	}
}

func assertXLSXRaw(t *testing.T, book *excelize.File, sheet, cell, want string) {
	t.Helper()
	raw, err := book.GetCellValue(sheet, cell, excelize.Options{RawCellValue: true})
	if err != nil || raw != want {
		t.Fatalf("%s %s=%q err=%v want=%q", sheet, cell, raw, err, want)
	}
}
