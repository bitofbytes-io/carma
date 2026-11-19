package ui

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/reminder"
	"github.com/google/uuid"
)

func TestRemindersTemplateUsesLabeledResponsiveStructure(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	vehicle := model.Vehicle{ID: uuid.New(), Nickname: "Ranger"}
	serviceType := model.ServiceType{ID: uuid.New(), Name: "Oil change"}
	reminderID := uuid.New()
	months := 6
	row := reminder.Result{Reminder: model.Reminder{ID: reminderID, VehicleID: vehicle.ID, ServiceTypeID: serviceType.ID, ServiceTypeName: serviceType.Name, IntervalMonths: &months, Enabled: true, CreatedAt: time.Now()}, Status: reminder.OK}
	type data struct {
		Title            string
		Error            string
		Flash            string
		Authenticated    bool
		User             *model.User
		NavVehicles      []model.Vehicle
		Vehicle          model.Vehicle
		Types            []model.ServiceType
		Reminders        []reminder.Result
		SnoozeReminderID uuid.UUID
	}
	var b bytes.Buffer
	if err := r.Render(&b, "reminders", data{Authenticated: true, User: &model.User{}, Vehicle: vehicle, Types: []model.ServiceType{serviceType}, Reminders: []reminder.Result{row}}); err != nil {
		t.Fatal(err)
	}
	body := b.String()
	for _, want := range []string{
		`class="reminder-row reminder-entry"`,
		`class="reminder-field reminder-enabled"`,
		`class="reminder-actions"`,
		`form="reminder-` + reminderID.String() + `"`,
		`<span class="field-label">Service type</span>`,
		`<span class="field-label">Every (months)</span>`,
		`<span class="field-label">Every (miles)</span>`,
		`aria-label="Enable Oil change reminder"`,
		`aria-label="Snooze Oil change reminder"`,
		`name="preset" value="2w"`,
		`>2 weeks</button>`,
		`name="preset" value="1m"`,
		`>1 month</button>`,
		`name="preset" value="500mi"`,
		`>+500 mi</button>`,
		`name="preset" value="custom"`,
		`aria-label="Snooze Oil change until date"`,
		`aria-label="Snooze Oil change until mileage"`,
		`<label><span>Service type</span><select name="service_type_id"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing responsive/accessibility structure %q in %s", want, body)
		}
	}

	header := reminderHeaderRe.FindStringSubmatch(body)
	if header == nil {
		t.Fatalf("missing reminder header row in %s", body)
	}
	if !strings.Contains(header[1], `aria-hidden="true"`) {
		t.Fatalf("reminder header is not aria-hidden: %q", header[1])
	}
	if !strings.Contains(header[2], `class="reminder-header-fields"`) {
		t.Fatalf("reminder header is missing its fields grid wrapper: %q", header[2])
	}
	var labels, classes []string
	for _, m := range headerSpanRe.FindAllStringSubmatch(header[2], -1) {
		classes = append(classes, m[1])
		labels = append(labels, strings.TrimSpace(m[2]))
	}
	wantLabels := []string{"Service type", "Every (months)", "Every (miles)", "Status", "Enabled", "Actions"}
	if strings.Join(labels, "|") != strings.Join(wantLabels, "|") {
		t.Fatalf("reminder header labels = %q, want %q", labels, wantLabels)
	}
	if !strings.Contains(classes[3], "reminder-header-status") || !strings.Contains(classes[4], "reminder-header-enabled") {
		t.Fatalf("reminder header status/enabled classes = %q", classes)
	}
}

var (
	// The header row holds a nested div, so match up to the row's own closing tags.
	reminderHeaderRe = regexp.MustCompile(`(?s)<div class="reminder-row header"([^>]*)>(.*?</div>\s*<span>Actions</span>)\s*</div>`)
	headerSpanRe     = regexp.MustCompile(`(?s)<span([^>]*)>(.*?)</span>`)
)
