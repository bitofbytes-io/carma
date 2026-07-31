package export

import (
	"encoding/csv"
	"fmt"
	"github.com/bitofbytes-io/carma/internal/model"
	"io"
	"strconv"
	"unicode"
)

func CSV(w io.Writer, records []model.Record) error {
	c := csv.NewWriter(w)
	defer c.Flush()
	if err := c.Write([]string{"vehicle", "date", "service type", "odometer", "cost", "vendor", "notes", "receipt count", "logged by"}); err != nil {
		return err
	}
	for _, r := range records {
		odo, cost := "", ""
		if r.OdometerMiles != nil {
			odo = strconv.FormatInt(*r.OdometerMiles, 10)
		}
		if r.CostCents != nil {
			cost = fmt.Sprintf("%.2f", float64(*r.CostCents)/100)
		}
		if err := c.Write([]string{spreadsheetText(r.VehicleName), r.OccurredOn.Format("2006-01-02"), spreadsheetText(r.ServiceTypeName), odo, cost, spreadsheetText(r.Vendor), spreadsheetText(r.Notes), strconv.Itoa(r.AttachmentCount), spreadsheetText(r.CreatedByName)}); err != nil {
			return err
		}
	}
	return c.Error()
}

func spreadsheetText(value string) string {
	for _, character := range value {
		if unicode.IsSpace(character) {
			continue
		}
		switch character {
		case '=', '+', '-', '@':
			return "'" + value
		default:
			return value
		}
	}
	return value
}
