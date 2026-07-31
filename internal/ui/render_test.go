package ui

import (
	"bytes"
	"net/url"
	"strings"
	"testing"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

func TestBaseTemplateDeclaresSupportedColorSchemes(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	type data struct {
		Title         string
		Authenticated bool
		Development   bool
		Error         string
		Flash         string
		Redirect      string
	}
	var b bytes.Buffer
	if err := r.Render(&b, "login", data{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `<meta name="color-scheme" content="light dark">`) {
		t.Fatal("base template does not advertise light and dark color schemes")
	}
}

func TestVehicleTemplateRendersThroughExportControls(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	type data struct {
		Title         string
		Flash         string
		Authenticated bool
		User          *model.User
		NavVehicles   []model.Vehicle
		Vehicle       model.Vehicle
		Params        url.Values
		Types         []model.ServiceType
		Records       []model.Record
		Reminders     []any
	}
	v := model.Vehicle{ID: uuid.New(), Nickname: "Outback"}
	var b bytes.Buffer
	err = r.Render(&b, "vehicle", data{Authenticated: true, User: &model.User{}, Vehicle: v, Params: url.Values{}, Types: []model.ServiceType{{ID: uuid.New(), Name: "Oil change"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "Export CSV") || !strings.Contains(b.String(), "Manage reminders") {
		t.Fatal("vehicle page rendered incompletely")
	}
	for _, name := range []string{"Search records", "Filter by service type", "From date", "To date", "Sort records by", "Sort direction"} {
		if !strings.Contains(b.String(), `aria-label="`+name+`"`) {
			t.Fatalf("missing accessible name %q", name)
		}
	}
}
