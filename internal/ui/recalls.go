package ui

import (
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

// RecallAlert is a dashboard "Needs attention" row for a vehicle with open
// NHTSA recalls. ParkIt is set when any of them says not to drive it.
type RecallAlert struct {
	VehicleID   uuid.UUID
	VehicleName string
	Count       int
	ParkIt      bool
}

// GroupRecalls turns open recalls into one alert per vehicle, in the order the
// vehicles first appear.
func GroupRecalls(recalls []model.Recall) []RecallAlert {
	var alerts []RecallAlert
	index := map[uuid.UUID]int{}
	for _, recall := range recalls {
		i, ok := index[recall.VehicleID]
		if !ok {
			i = len(alerts)
			index[recall.VehicleID] = i
			alerts = append(alerts, RecallAlert{VehicleID: recall.VehicleID, VehicleName: recall.VehicleName})
		}
		alerts[i].Count++
		alerts[i].ParkIt = alerts[i].ParkIt || recall.ParkIt
	}
	return alerts
}
