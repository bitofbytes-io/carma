package export

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

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

func TestSheetTitle(t *testing.T) {
	emoji := "😀"
	cases := []struct {
		name    string
		vehicle string
		want    string
	}{
		{name: "leading apostrophe", vehicle: "'Outback", want: "Outback"},
		{name: "trailing apostrophe", vehicle: "Outback'", want: "Outback"},
		{name: "both edges", vehicle: "'Outback'", want: "Outback"},
		{name: "apostrophe inside the name", vehicle: "Dan's Outback", want: "Dan's Outback"},
		{name: "typographic apostrophe stays", vehicle: "’Outback’", want: "’Outback’"},
		{name: "single apostrophe", vehicle: "'", want: "Vehicle"},
		{name: "only apostrophes", vehicle: "''''", want: "Vehicle"},
		{name: "long run of apostrophes", vehicle: strings.Repeat("'", 40), want: "Vehicle"},
		{name: "apostrophes and spaces", vehicle: "  '   '  ", want: "Vehicle"},
		{name: "apostrophes and invalid characters", vehicle: "':/\\?*[]'", want: "Vehicle"},
		{name: "invalid characters", vehicle: "Tacoma/North?", want: "Tacoma North"},
		{name: "every invalid character", vehicle: `a:b\c/d?e*f[g]h`, want: "a b c d e f g h"},
		{name: "rune length over the limit", vehicle: strings.Repeat("A", 40), want: strings.Repeat("A", 31)},
		{name: "quotes around a long name", vehicle: "'" + strings.Repeat("A", 40) + "'", want: strings.Repeat("A", 31)},
		{name: "truncation exposes a trailing apostrophe", vehicle: strings.Repeat("A", 30) + "'B", want: strings.Repeat("A", 30)},
		{name: "space then apostrophe at the cut", vehicle: strings.Repeat("A", 29) + " '" + "BCDE", want: strings.Repeat("A", 29)},
		{
			name:    "quotes, invalid characters, and the cut",
			vehicle: "'" + strings.Repeat("A", 30) + "'B/?'",
			want:    strings.Repeat("A", 30),
		},
		{
			name:    "leading quote and an invalid character inside the limit",
			vehicle: "'Hello/World " + strings.Repeat("A", 20) + "'B",
			want:    "Hello World " + strings.Repeat("A", 19),
		},
		{name: "utf-16 length", vehicle: strings.Repeat(emoji, 16), want: strings.Repeat(emoji, 15)},
		{name: "utf-16 cut exposes an apostrophe", vehicle: strings.Repeat(emoji, 15) + "'Z", want: strings.Repeat(emoji, 15)},
		{name: "quotes around a long emoji name", vehicle: "'" + strings.Repeat(emoji, 20) + "'/?", want: strings.Repeat(emoji, 15)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sheetTitle(tc.vehicle)
			if got != tc.want {
				t.Fatalf("sheetTitle(%q)=%q want %q", tc.vehicle, got, tc.want)
			}
			assertValidSheetName(t, got)
		})
	}
}

func TestUniqueSheetNameCombinesRules(t *testing.T) {
	used := map[string]struct{}{}
	cut := strings.Repeat("A", 30) + "'B"
	first := uniqueSheetName(cut, used)
	second := uniqueSheetName(cut, used)
	plain := uniqueSheetName(strings.Repeat("A", 30), used)
	if first != strings.Repeat("A", 30) || second != strings.Repeat("A", 27)+" (2)" || plain != strings.Repeat("A", 27)+" (3)" {
		t.Fatalf("cut names %q %q %q", first, second, plain)
	}

	boundary := strings.Repeat("A", 26) + "'" + strings.Repeat("B", 10)
	left := uniqueSheetName(boundary, used)
	right := uniqueSheetName(boundary, used)
	if left != strings.Repeat("A", 26)+"'"+strings.Repeat("B", 4) || right != strings.Repeat("A", 26)+" (2)" {
		t.Fatalf("boundary %q %q", left, right)
	}

	only := uniqueSheetName(strings.Repeat("'", 8), used)
	named := uniqueSheetName("Vehicle", used)
	folded := uniqueSheetName("vehicle", used)
	if only != "Vehicle" || named != "Vehicle (2)" || folded != "vehicle (3)" {
		t.Fatalf("fallback %q %q %q", only, named, folded)
	}

	emojiCut := strings.Repeat("😀", 15) + "'Z"
	emoji := uniqueSheetName(emojiCut, used)
	emojiDup := uniqueSheetName(emojiCut, used)
	if emoji != strings.Repeat("😀", 15) || emojiDup != strings.Repeat("😀", 13)+" (2)" {
		t.Fatalf("emoji %q %q", emoji, emojiDup)
	}

	combo := "'" + strings.Repeat("C", 30) + "'D/?'"
	var grown []string
	for range 12 {
		grown = append(grown, uniqueSheetName(combo, used))
	}
	if grown[0] != strings.Repeat("C", 30) || grown[1] != strings.Repeat("C", 27)+" (2)" || grown[9] != strings.Repeat("C", 26)+" (10)" {
		t.Fatalf("grown %q", grown)
	}

	seen := map[string]struct{}{}
	for _, sheet := range append([]string{first, second, plain, left, right, only, named, folded, emoji, emojiDup}, grown...) {
		assertValidSheetName(t, sheet)
		key := strings.ToLower(sheet)
		if _, ok := seen[key]; ok {
			t.Fatalf("duplicate sheet %q", sheet)
		}
		seen[key] = struct{}{}
	}
}

func TestXLSXAwkwardVehicleNames(t *testing.T) {
	cut := strings.Repeat("A", 30) + "'B"
	names := []string{
		"'",
		"''''",
		cut,
		cut,
		"'" + strings.Repeat("C", 30) + "'D/?'",
		"Dan's Outback",
		"'Outback'",
		"  ' : '  ",
	}
	wants := []string{
		"Vehicle",
		"Vehicle (2)",
		strings.Repeat("A", 30),
		strings.Repeat("A", 27) + " (2)",
		strings.Repeat("C", 30),
		"Dan's Outback",
		"Outback",
		"Vehicle (3)",
	}
	records := make([]model.Record, len(names))
	for i, name := range names {
		records[i] = model.Record{
			VehicleID:   uuid.New(),
			VehicleName: name,
			OccurredOn:  time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		}
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
	if strings.Join(sheets, "\n") != strings.Join(wants, "\n") {
		t.Fatalf("sheets=%q", sheets)
	}
	seen := map[string]struct{}{}
	for _, sheet := range sheets {
		assertValidSheetName(t, sheet)
		key := strings.ToLower(sheet)
		if _, ok := seen[key]; ok {
			t.Fatalf("duplicate sheet %q", sheet)
		}
		seen[key] = struct{}{}
		rows, err := book.GetRows(sheet)
		if err != nil || len(rows) != 3 || rows[2][0] != "Total" {
			t.Fatalf("sheet %q rows=%v err=%v", sheet, rows, err)
		}
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

func assertValidSheetName(t *testing.T, name string) {
	t.Helper()
	if strings.Trim(name, "' ") == "" {
		t.Fatalf("blank sheet name %q", name)
	}
	if strings.HasPrefix(name, "'") || strings.HasSuffix(name, "'") {
		t.Fatalf("sheet name %q starts or ends with an apostrophe", name)
	}
	if strings.ContainsAny(name, `:\/?*[]`) {
		t.Fatalf("sheet name %q contains invalid characters", name)
	}
	if len(utf16.Encode([]rune(name))) > xlsxSheetLimit {
		t.Fatalf("sheet name %q is longer than %d UTF-16 units", name, xlsxSheetLimit)
	}
}

func cellStyle(book *excelize.File, sheet, cell string) (*excelize.Style, error) {
	id, err := book.GetCellStyle(sheet, cell)
	if err != nil {
		return nil, err
	}
	return book.GetStyle(id)
}
