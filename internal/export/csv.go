package export

import (
	"encoding/csv"
	"fmt"
	"github.com/bitofbytes-io/carma/internal/model"
	"io"
	"strconv"
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
		if err := c.Write([]string{r.VehicleName, r.OccurredOn.Format("2006-01-02"), r.ServiceTypeName, odo, cost, r.Vendor, r.Notes, strconv.Itoa(r.AttachmentCount), r.CreatedByName}); err != nil {
			return err
		}
	}
	return c.Error()
}
