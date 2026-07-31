package export

import (
	"encoding/csv"
	"github.com/bitofbytes-io/carma/internal/model"
	"io"
	"strconv"
	"unicode"
)

func CSV(w io.Writer, records []model.Record) error {
	c := csv.NewWriter(w)
	if err := c.Write([]string{"vehicle", "date", "service type", "odometer", "cost", "vendor", "notes", "receipt count", "logged by"}); err != nil {
		return err
	}
	for _, r := range records {
		odo, cost := "", ""
		if r.OdometerMiles != nil {
			odo = strconv.FormatInt(*r.OdometerMiles, 10)
		}
		if r.CostCents != nil {
			cost = formatCents(*r.CostCents)
		}
		if err := c.Write([]string{spreadsheetText(r.VehicleName), r.OccurredOn.Format("2006-01-02"), spreadsheetText(r.ServiceTypeName), odo, cost, spreadsheetText(r.Vendor), spreadsheetText(r.Notes), strconv.Itoa(r.AttachmentCount), spreadsheetText(r.CreatedByName)}); err != nil {
			return err
		}
	}
	c.Flush()
	return c.Error()
}

func formatCents(cents int64) string {
	whole, remainder := cents/100, cents%100
	sign := ""
	if cents < 0 {
		sign = "-"
		whole = -whole
		remainder = -remainder
	}
	fraction := strconv.FormatInt(remainder+100, 10)[1:]
	return sign + strconv.FormatInt(whole, 10) + "." + fraction
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
