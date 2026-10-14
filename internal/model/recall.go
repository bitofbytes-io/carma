package model

import (
	"time"

	"github.com/google/uuid"
)

// Recall is an NHTSA safety recall campaign for a vehicle's model year, make,
// and model. Campaigns are not VIN-specific, so a household can dismiss one
// that is already fixed or does not apply.
type Recall struct {
	VehicleID                               uuid.UUID
	VehicleName                             string
	CampaignNumber                          string
	Component, Summary, Consequence, Remedy string
	ReportReceivedOn                        *time.Time
	// ParkIt and ParkOutside are NHTSA's "do not drive" and "park outside"
	// warnings.
	ParkIt, ParkOutside bool
	FirstSeenAt         time.Time
	DismissedAt         *time.Time
}
