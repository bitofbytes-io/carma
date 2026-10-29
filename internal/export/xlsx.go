package export

import (
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"
)

const (
	xlsxDateFormat = "yyyy-mm-dd"
	xlsxCostFormat = "$#,##0.00;-$#,##0.00"
	xlsxSheetLimit = 31
)

const (
	colDate = iota + 1
	colOdometer
	colService
	colVendor
	colCost
)

// XLSX writes one worksheet per vehicle. Columns are date, odometer, service,
// vendor, and cost. Date and cost are Excel date and currency values, and each
// sheet ends with the cost total from Summarize. Records stay in the order
// given, which is oldest first when they come from Query.
func XLSX(w io.Writer, records []model.Record) error {
	file := excelize.NewFile()
	defer func() { _ = file.Close() }()

	dateFormat, costFormat := xlsxDateFormat, xlsxCostFormat
	dateStyle, err := file.NewStyle(&excelize.Style{CustomNumFmt: &dateFormat})
	if err != nil {
		return err
	}
	costStyle, err := file.NewStyle(&excelize.Style{CustomNumFmt: &costFormat})
	if err != nil {
		return err
	}
	totalCostStyle, err := file.NewStyle(&excelize.Style{CustomNumFmt: &costFormat, Font: &excelize.Font{Bold: true}})
	if err != nil {
		return err
	}
	headerStyle, err := file.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return err
	}
	odometerStyle, err := file.NewStyle(&excelize.Style{NumFmt: 3})
	if err != nil {
		return err
	}

	groups := groupByVehicle(records)
	used := map[string]struct{}{}
	for i, group := range groups {
		sheet := uniqueSheetName(group.name, used)
		if i == 0 {
			if err = file.SetSheetName("Sheet1", sheet); err != nil {
				return err
			}
		} else if _, err = file.NewSheet(sheet); err != nil {
			return err
		}
		if err = writeVehicleSheet(file, sheet, group.records, dateStyle, costStyle, totalCostStyle, headerStyle, odometerStyle); err != nil {
			return err
		}
	}
	return file.Write(w)
}

type vehicleGroup struct {
	name    string
	records []model.Record
}

func groupByVehicle(records []model.Record) []vehicleGroup {
	if len(records) == 0 {
		return []vehicleGroup{{name: "Service history"}}
	}
	order := map[uuid.UUID]int{}
	var groups []vehicleGroup
	for _, record := range records {
		index, ok := order[record.VehicleID]
		if !ok {
			index = len(groups)
			order[record.VehicleID] = index
			name := strings.TrimSpace(record.VehicleName)
			if name == "" {
				name = "Vehicle"
			}
			groups = append(groups, vehicleGroup{name: name})
		}
		groups[index].records = append(groups[index].records, record)
	}
	return groups
}

func writeVehicleSheet(file *excelize.File, sheet string, records []model.Record, dateStyle, costStyle, totalCostStyle, headerStyle, odometerStyle int) error {
	for i, header := range []string{"Date", "Odometer", "Service", "Vendor", "Cost"} {
		cell, err := excelize.CoordinatesToCellName(i+1, 1)
		if err != nil {
			return err
		}
		if err = file.SetCellStr(sheet, cell, header); err != nil {
			return err
		}
	}
	if err := file.SetCellStyle(sheet, "A1", "E1", headerStyle); err != nil {
		return err
	}
	for i, record := range records {
		row := i + 2
		if err := writeRecordRow(file, sheet, row, record, dateStyle, costStyle, odometerStyle); err != nil {
			return err
		}
	}
	totalRow := len(records) + 2
	label, err := excelize.CoordinatesToCellName(colDate, totalRow)
	if err != nil {
		return err
	}
	if err = file.SetCellStr(sheet, label, "Total"); err != nil {
		return err
	}
	if err = file.SetCellStyle(sheet, label, label, headerStyle); err != nil {
		return err
	}
	summary := Summarize(records)
	if summary.TotalCents != nil {
		cell, err := excelize.CoordinatesToCellName(colCost, totalRow)
		if err != nil {
			return err
		}
		if err = writeCost(file, sheet, cell, *summary.TotalCents, totalCostStyle); err != nil {
			return err
		}
	}
	if err = file.SetColWidth(sheet, "A", "A", 14); err != nil {
		return err
	}
	if err = file.SetColWidth(sheet, "B", "B", 14); err != nil {
		return err
	}
	if err = file.SetColWidth(sheet, "C", "D", 24); err != nil {
		return err
	}
	return file.SetColWidth(sheet, "E", "E", 14)
}

func writeRecordRow(file *excelize.File, sheet string, row int, record model.Record, dateStyle, costStyle, odometerStyle int) error {
	dateCell, err := excelize.CoordinatesToCellName(colDate, row)
	if err != nil {
		return err
	}
	occurred := record.OccurredOn
	day := time.Date(occurred.Year(), occurred.Month(), occurred.Day(), 0, 0, 0, 0, time.UTC)
	if err = file.SetCellValue(sheet, dateCell, day); err != nil {
		return err
	}
	if err = file.SetCellStyle(sheet, dateCell, dateCell, dateStyle); err != nil {
		return err
	}
	if record.OdometerMiles != nil {
		cell, err := excelize.CoordinatesToCellName(colOdometer, row)
		if err != nil {
			return err
		}
		if err = file.SetCellInt(sheet, cell, *record.OdometerMiles); err != nil {
			return err
		}
		if err = file.SetCellStyle(sheet, cell, cell, odometerStyle); err != nil {
			return err
		}
	}
	serviceCell, err := excelize.CoordinatesToCellName(colService, row)
	if err != nil {
		return err
	}
	if err = file.SetCellStr(sheet, serviceCell, record.ServiceTypeName); err != nil {
		return err
	}
	vendorCell, err := excelize.CoordinatesToCellName(colVendor, row)
	if err != nil {
		return err
	}
	if err = file.SetCellStr(sheet, vendorCell, record.Vendor); err != nil {
		return err
	}
	if record.CostCents != nil {
		cell, err := excelize.CoordinatesToCellName(colCost, row)
		if err != nil {
			return err
		}
		if err = writeCost(file, sheet, cell, *record.CostCents, costStyle); err != nil {
			return err
		}
	}
	return nil
}

func writeCost(file *excelize.File, sheet, cell string, cents int64, style int) error {
	if err := file.SetCellDefault(sheet, cell, formatCents(cents)); err != nil {
		return err
	}
	return file.SetCellStyle(sheet, cell, cell, style)
}

func uniqueSheetName(name string, used map[string]struct{}) string {
	base := sheetTitle(name)
	candidate := base
	for n := 2; ; n++ {
		if _, ok := used[strings.ToLower(candidate)]; !ok {
			used[strings.ToLower(candidate)] = struct{}{}
			return candidate
		}
		suffix := fmt.Sprintf(" (%d)", n)
		room := xlsxSheetLimit - len(utf16.Encode([]rune(suffix)))
		// Shortening the base to fit the suffix can expose an apostrophe again.
		prefix := trimSheetEdges(truncateUTF16(base, room))
		candidate = prefix + suffix
	}
}

func sheetTitle(name string) string {
	var builder strings.Builder
	for _, character := range name {
		switch character {
		case ':', '\\', '/', '?', '*', '[', ']':
			builder.WriteByte(' ')
		default:
			builder.WriteRune(character)
		}
	}
	cleaned := trimSheetEdges(strings.Join(strings.Fields(builder.String()), " "))
	if cleaned == "" {
		return "Vehicle"
	}
	if len(utf16.Encode([]rune(cleaned))) <= xlsxSheetLimit {
		return cleaned
	}
	// The cut can land on an apostrophe or a space. Trim again so the name still
	// does not start or end with "'", and is not blank.
	cleaned = trimSheetEdges(truncateUTF16(cleaned, xlsxSheetLimit))
	if cleaned == "" {
		return "Vehicle"
	}
	return cleaned
}

func trimSheetEdges(name string) string {
	return strings.Trim(name, "' ")
}

func truncateUTF16(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	units := 0
	var builder strings.Builder
	for _, character := range value {
		width := 1
		if character > 0xFFFF {
			width = 2
		}
		if units+width > limit {
			break
		}
		units += width
		builder.WriteRune(character)
	}
	return builder.String()
}
