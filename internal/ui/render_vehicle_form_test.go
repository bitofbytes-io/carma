package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

func TestVehicleFormShowsExistingPhotoOnlyWhenEditingSavedPhoto(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	vehicleID := uuid.New()
	tests := []struct {
		name         string
		editing      bool
		vehicle      model.Vehicle
		wantPreview  bool
		wantOptional bool
	}{
		{
			name:         "edit with saved photo",
			editing:      true,
			vehicle:      model.Vehicle{ID: vehicleID, Nickname: "Outback", PhotoKey: "saved/photo.jpg"},
			wantPreview:  true,
			wantOptional: false,
		},
		{
			name:         "edit without saved photo",
			editing:      true,
			vehicle:      model.Vehicle{ID: vehicleID, Nickname: "Outback"},
			wantPreview:  false,
			wantOptional: true,
		},
		{
			name:         "create ignores stale photo data",
			vehicle:      model.Vehicle{ID: vehicleID, Nickname: "Outback", PhotoKey: "stale/photo.jpg"},
			wantPreview:  false,
			wantOptional: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			type data struct {
				Title, Error, Flash string
				Authenticated       bool
				Editing             bool
				Vehicle             model.Vehicle
				ReminderPresets     []ReminderPreset

				VINDecode  bool
				VINMessage string
				VINError   bool
			}
			var b bytes.Buffer
			if err := r.Render(&b, "vehicle-form", data{Editing: tt.editing, Vehicle: tt.vehicle}); err != nil {
				t.Fatal(err)
			}
			body := b.String()
			for _, want := range []string{
				`<label for="vehicle-photo">Photo</label>`,
				`id="vehicle-photo" type="file" name="photo"`,
				`aria-describedby="vehicle-photo-help"`,
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("missing photo input markup %q in %s", want, body)
				}
			}
			inputStart := strings.Index(body, `id="vehicle-photo"`)
			if inputStart < 0 {
				t.Fatalf("photo input not found in %s", body)
			}
			inputEnd := strings.Index(body[inputStart:], ">")
			if inputEnd < 0 {
				t.Fatalf("photo input is incomplete in %s", body)
			}
			if input := body[inputStart : inputStart+inputEnd]; strings.Contains(input, "value=") {
				t.Fatalf("file input was populated: %s", input)
			}

			previewMarkers := []string{
				`class="existing-photo"`,
				`src="/vehicles/` + vehicleID.String() + `/photo"`,
				"Current photo",
				"This photo is already saved. Choose a new file to replace it.",
			}
			for _, marker := range previewMarkers {
				if got := strings.Contains(body, marker); got != tt.wantPreview {
					t.Fatalf("preview marker %q present=%v, want %v in %s", marker, got, tt.wantPreview, body)
				}
			}
			if got := strings.Contains(body, "Optional. Add a photo of your vehicle."); got != tt.wantOptional {
				t.Fatalf("optional guidance present=%v, want %v in %s", got, tt.wantOptional, body)
			}
		})
	}
}

func TestVehicleFormOffersReminderPresetsOnlyWhenAdding(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	type data struct {
		Title, Error, Flash string
		Authenticated       bool
		Editing             bool
		Vehicle             model.Vehicle
		ReminderPresets     []ReminderPreset

		VINDecode  bool
		VINMessage string
		VINError   bool
	}
	presets := ReminderPresets([]string{"oil change"})
	var b bytes.Buffer
	if err := r.Render(&b, "vehicle-form", data{ReminderPresets: presets}); err != nil {
		t.Fatal(err)
	}
	body := b.String()
	for _, want := range []string{
		"Starter reminders",
		`name="reminder_preset" value="Oil change" checked>`,
		`name="reminder_preset" value="Tire rotation" >`,
		"every 6 months or 5,000 mi",
		"every 7,500 mi",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("add form missing %q in %s", want, body)
		}
	}

	b.Reset()
	if err := r.Render(&b, "vehicle-form", data{Editing: true, Vehicle: model.Vehicle{ID: uuid.New(), Nickname: "Wagon"}, ReminderPresets: presets}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "reminder_preset") {
		t.Fatalf("edit form offers reminder presets: %s", b.String())
	}
}

func TestVehicleIdentityOffersVINDecodeOnlyWhenEnabled(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	type identity struct {
		Vehicle             model.Vehicle
		VINDecode, VINError bool
		VINMessage          string
	}
	var b bytes.Buffer
	if err = r.RenderNamed(&b, "vehicle-form", "vehicle-identity", identity{Vehicle: model.Vehicle{VIN: "1HGCM82633A004352"}}); err != nil {
		t.Fatal(err)
	}
	if body := b.String(); strings.Contains(body, "Decode VIN") || !strings.Contains(body, `value="1HGCM82633A004352"`) {
		t.Fatalf("decode disabled: %s", body)
	}
	b.Reset()
	if err = r.RenderNamed(&b, "vehicle-form", "vehicle-identity", identity{VINDecode: true, VINError: true, VINMessage: "NHTSA could not decode this VIN."}); err != nil {
		t.Fatal(err)
	}
	body := b.String()
	for _, want := range []string{`id="vehicle-identity"`, `hx-post="/vehicles/vin-decode"`, `hx-target="#vehicle-identity"`, `type="button"`, `class="vin-message full vin-error"`, "could not decode"} {
		if !strings.Contains(body, want) {
			t.Fatalf("decode enabled missing %q: %s", want, body)
		}
	}
}
