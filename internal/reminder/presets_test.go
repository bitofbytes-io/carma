package reminder

import "testing"

func TestPresetInterval(t *testing.T) {
	tests := []struct {
		preset Preset
		want   string
	}{
		{Preset{Months: 6, Miles: 5000}, "every 6 months or 5,000 mi"},
		{Preset{Miles: 7500}, "every 7,500 mi"},
		{Preset{Months: 12}, "every 12 months"},
		{Preset{Months: 1}, "every 1 month"},
	}
	for _, test := range tests {
		if got := test.preset.Interval(); got != test.want {
			t.Errorf("%+v.Interval() = %q, want %q", test.preset, got, test.want)
		}
	}
}

func TestFindPresetIgnoresCaseAndRejectsUnknown(t *testing.T) {
	preset, ok := FindPreset(" oil CHANGE ")
	if !ok || preset.ServiceType != "Oil change" || preset.Months != 6 || preset.Miles != 5000 {
		t.Fatalf("FindPreset = %+v, %v", preset, ok)
	}
	if _, ok := FindPreset("Repair"); ok {
		t.Fatal("Repair should not be a preset")
	}
}

func TestPresetsHaveAnInterval(t *testing.T) {
	for _, preset := range Presets {
		if preset.Months <= 0 && preset.Miles <= 0 {
			t.Errorf("preset %q has no interval", preset.ServiceType)
		}
	}
}
