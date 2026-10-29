package export

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"
)

func TestXLSXReadsBackTypedRowsAndTotals(t *testing.T) {
	outback, tacoma := uuid.New(), uuid.New()
	oil, tires, driveway := int64(8900), int64(45050), int64(0)
	miles := int64(55000)
	laterMiles := int64(61000)
	records := []model.Record{
		{VehicleID: outback, VehicleName: "Outback", OccurredOn: time.Date(2025, 11, 14, 22, 0, 0, 0, time.FixedZone("EDT", -4*3600)), ServiceTypeName: "Oil change", OdometerMiles: &miles, CostCents: &oil, Vendor: "Jiffy Lube"},
		{VehicleID: outback, VehicleName: "Outback", OccurredOn: time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC), ServiceTypeName: "Tire rotation", Vendor: "=cmd|' /C calc'!A0"},
		{VehicleID: tacoma, VehicleName: "Tacoma/North?", OccurredOn: time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC), ServiceTypeName: "Tires", OdometerMiles: &laterMiles, CostCents: &tires, Vendor: "Tire Barn"},
		{VehicleID: tacoma, VehicleName: "Tacoma/North?", OccurredOn: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), ServiceTypeName: "Inspection", CostCents: &driveway, Vendor: "DIY"},
	}

	var buf bytes.Buffer
	if err := XLSX(&buf, records); err != nil {
		t.Fatal(err)
	}
	book, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = book.Close() })

	sheets := book.GetSheetList()
	if len(sheets) != 2 || sheets[0] != "Outback" || sheets[1] != "Tacoma North" {
		t.Fatalf("sheets=%v", sheets)
	}

	rows, err := book.GetRows("Outback")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 || strings.Join(rows[0], "|") != "Date|Odometer|Service|Vendor|Cost" {
		t.Fatalf("header rows=%v", rows)
	}
	assertDateCell(t, book, "Outback", "A2", "2025-11-14")
	assertNumber(t, book, "Outback", "B2", "55000")
	assertText(t, book, "Outback", "C2", "Oil change")
	assertText(t, book, "Outback", "D3", "=cmd|' /C calc'!A0")
	assertCurrency(t, book, "Outback", "E2", "89.00", false)
	if value, err := book.GetCellValue("Outback", "B3"); err != nil || value != "" {
		t.Fatalf("missing odometer=%q err=%v", value, err)
	}
	if value, err := book.GetCellValue("Outback", "E3"); err != nil || value != "" {
		t.Fatalf("missing cost=%q err=%v", value, err)
	}
	assertCurrency(t, book, "Outback", "E4", "89.00", true)
	if rows[3][0] != "Total" {
		t.Fatalf("total label=%q", rows[3][0])
	}

	assertDateCell(t, book, "Tacoma North", "A2", "2026-05-02")
	assertCurrency(t, book, "Tacoma North", "E2", "450.50", false)
	assertCurrency(t, book, "Tacoma North", "E3", "0.00", false)
	assertCurrency(t, book, "Tacoma North", "E4", "450.50", true)
}

func TestXLSXEmptyWorkbookAndDuplicateSheetNames(t *testing.T) {
	var empty bytes.Buffer
	if err := XLSX(&empty, nil); err != nil {
		t.Fatal(err)
	}
	book, err := excelize.OpenReader(bytes.NewReader(empty.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = book.Close() })
	rows, err := book.GetRows("Service history")
	if err != nil || len(rows) != 2 || rows[1][0] != "Total" {
		t.Fatalf("empty rows=%v err=%v sheets=%v", rows, err, book.GetSheetList())
	}
	if value, err := book.GetCellValue("Service history", "E2"); err != nil || value != "" {
		t.Fatalf("empty total cost=%q err=%v", value, err)
	}

	long := strings.Repeat("A", 40)
	id1, id2 := uuid.New(), uuid.New()
	var dup bytes.Buffer
	records := []model.Record{
		{VehicleID: id1, VehicleName: long, OccurredOn: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		{VehicleID: id2, VehicleName: long, OccurredOn: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)},
		{VehicleID: uuid.Nil, VehicleName: "  ", OccurredOn: time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC)},
	}
	if err = XLSX(&dup, records); err != nil {
		t.Fatal(err)
	}
	named, err := excelize.OpenReader(bytes.NewReader(dup.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = named.Close() })
	sheets := named.GetSheetList()
	if len(sheets) != 3 || sheets[0] != strings.Repeat("A", 31) || sheets[1] != strings.Repeat("A", 27)+" (2)" || sheets[2] != "Vehicle" {
		t.Fatalf("sheets=%v", sheets)
	}
}

func TestXLSXPropagatesWriteError(t *testing.T) {
	want := errWrite
	if err := XLSX(failingWriter{err: want}, nil); err == nil {
		t.Fatal("expected write error")
	}
}

var errWrite = writeError{}

type writeError struct{}

func (writeError) Error() string { return "write failed" }

func assertDateCell(t *testing.T, book *excelize.File, sheet, cell, want string) {
	t.Helper()
	raw, err := book.GetCellValue(sheet, cell, excelize.Options{RawCellValue: true})
	if err != nil {
		t.Fatal(err)
	}
	serial, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		t.Fatalf("%s %s raw=%q err=%v", sheet, cell, raw, err)
	}
	got, err := excelize.ExcelDateToTime(serial, false)
	if err != nil || got.Format("2006-01-02") != want {
		t.Fatalf("%s %s date=%v raw=%s err=%v want=%s", sheet, cell, got, raw, err, want)
	}
	style, err := cellStyle(book, sheet, cell)
	if err != nil || style.CustomNumFmt == nil || *style.CustomNumFmt != xlsxDateFormat {
		t.Fatalf("%s %s date format=%v err=%v", sheet, cell, style, err)
	}
	kind, err := book.GetCellType(sheet, cell)
	if err != nil || kind == excelize.CellTypeSharedString || kind == excelize.CellTypeInlineString {
		t.Fatalf("%s %s date stored as text type=%v err=%v", sheet, cell, kind, err)
	}
}

func assertCurrency(t *testing.T, book *excelize.File, sheet, cell, want string, total bool) {
	t.Helper()
	raw, err := book.GetCellValue(sheet, cell, excelize.Options{RawCellValue: true})
	if err != nil || raw != want {
		t.Fatalf("%s %s cost=%q err=%v want=%q", sheet, cell, raw, err, want)
	}
	style, err := cellStyle(book, sheet, cell)
	if err != nil || style.CustomNumFmt == nil || *style.CustomNumFmt != xlsxCostFormat {
		t.Fatalf("%s %s cost format=%v err=%v", sheet, cell, style, err)
	}
	if total && (style.Font == nil || !style.Font.Bold) {
		t.Fatalf("%s %s total cost is not bold", sheet, cell)
	}
	kind, err := book.GetCellType(sheet, cell)
	if err != nil || kind == excelize.CellTypeSharedString || kind == excelize.CellTypeInlineString {
		t.Fatalf("%s %s cost stored as text type=%v err=%v", sheet, cell, kind, err)
	}
}

func assertNumber(t *testing.T, book *excelize.File, sheet, cell, want string) {
	t.Helper()
	raw, err := book.GetCellValue(sheet, cell, excelize.Options{RawCellValue: true})
	if err != nil || raw != want {
		t.Fatalf("%s %s number=%q err=%v want=%q", sheet, cell, raw, err, want)
	}
}

func assertText(t *testing.T, book *excelize.File, sheet, cell, want string) {
	t.Helper()
	kind, err := book.GetCellType(sheet, cell)
	if err != nil || kind != excelize.CellTypeSharedString {
		t.Fatalf("%s %s type=%v err=%v", sheet, cell, kind, err)
	}
	got, err := book.GetCellValue(sheet, cell)
	if err != nil || got != want {
		t.Fatalf("%s %s text=%q err=%v want=%q", sheet, cell, got, err, want)
	}
}

func cellStyle(book *excelize.File, sheet, cell string) (*excelize.Style, error) {
	id, err := book.GetCellStyle(sheet, cell)
	if err != nil {
		return nil, err
	}
	return book.GetStyle(id)
}
