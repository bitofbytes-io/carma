package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/reminder"
	"github.com/google/uuid"
)

func TestDashboardOdometerFormSitsOutsideCardLink(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	miles := int64(62410)
	vehicle := model.Vehicle{ID: uuid.New(), Nickname: "Wagon", LatestOdometer: &miles}
	type data struct {
		Title, Error, Flash string
		Authenticated       bool
		User                *model.User
		NavVehicles         []model.Vehicle
		Vehicles            []model.Vehicle
		Attention           []reminder.Result
		OdometerForm        OdometerForm

		RecallAlerts []RecallAlert
	}
	var b bytes.Buffer
	if err := r.Render(&b, "dashboard", data{Authenticated: true, User: &model.User{}, Vehicles: []model.Vehicle{vehicle}}); err != nil {
		t.Fatal(err)
	}
	body := b.String()
	card := body[strings.Index(body, `id="vehicle-card-`+vehicle.ID.String()+`"`):]
	link, form := strings.Index(card, `<a class="vehicle-link"`), strings.Index(card, `<form class="odometer-form"`)
	if link < 0 || form < 0 || !strings.Contains(card[link:form], "</a>") {
		t.Fatalf("mileage form is not after the card link: %s", card)
	}
	for _, want := range []string{"62410 mi", `name="return" value="dashboard"`, `aria-label="Current mileage for Wagon"`, `id="attention"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard missing %s", want)
		}
	}

	b.Reset()
	update := data{OdometerForm: OdometerForm{Vehicle: vehicle, Return: "dashboard"}}
	if err := r.RenderNamed(&b, "dashboard", "odometer-update", update); err != nil {
		t.Fatal(err)
	}
	if body = b.String(); !strings.Contains(body, `id="vehicle-card-`+vehicle.ID.String()+`"`) || !strings.Contains(body, `<div hx-swap-oob="innerHTML:#attention">`) || strings.Contains(body, "<html") {
		t.Fatalf("odometer update partial: %s", body)
	}
}
