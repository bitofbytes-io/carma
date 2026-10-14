package reminder

import (
	"strconv"
	"strings"
)

// Preset is a common maintenance interval offered when adding a vehicle. Its
// ServiceType matches a seeded service type name; zero intervals are unset.
type Preset struct {
	ServiceType string
	Months      int
	Miles       int64
}

// Presets are the starter reminders offered on the new-vehicle form.
var Presets = []Preset{
	{ServiceType: "Oil change", Months: 6, Miles: 5000},
	{ServiceType: "Tire rotation", Miles: 7500},
	{ServiceType: "Engine air filter", Miles: 15000},
	{ServiceType: "Cabin air filter", Months: 12},
	{ServiceType: "Wipers", Months: 12},
	{ServiceType: "Registration/inspection", Months: 12},
}

// FindPreset returns the preset for a service type name, ignoring case.
func FindPreset(serviceType string) (Preset, bool) {
	for _, preset := range Presets {
		if strings.EqualFold(preset.ServiceType, strings.TrimSpace(serviceType)) {
			return preset, true
		}
	}
	return Preset{}, false
}

// Interval describes the preset's interval, e.g. "every 6 months or 5,000 mi".
func (p Preset) Interval() string {
	var parts []string
	if p.Months == 1 {
		parts = append(parts, "1 month")
	} else if p.Months > 0 {
		parts = append(parts, strconv.Itoa(p.Months)+" months")
	}
	if p.Miles > 0 {
		parts = append(parts, groupThousands(p.Miles)+" mi")
	}
	return "every " + strings.Join(parts, " or ")
}
