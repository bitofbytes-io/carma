package ui

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/nhtsa"
	"github.com/bitofbytes-io/carma/internal/reminder"
)

//go:embed templates/*.html
var files embed.FS

type Renderer struct{ pages map[string]*template.Template }

// OdometerForm is the quick mileage update control for one vehicle. Return is
// the page showing it: "dashboard" or "vehicle".
type OdometerForm struct {
	Vehicle      model.Vehicle
	Return       string
	Value, Error string
	Confirm      bool
}

// ReminderPreset is a starter reminder checkbox on the new-vehicle form.
type ReminderPreset struct {
	reminder.Preset
	Checked bool
}

// ReminderPresets lists every preset, checking those named in selected.
func ReminderPresets(selected []string) []ReminderPreset {
	options := make([]ReminderPreset, 0, len(reminder.Presets))
	for _, preset := range reminder.Presets {
		option := ReminderPreset{Preset: preset}
		for _, name := range selected {
			if found, ok := reminder.FindPreset(name); ok && found.ServiceType == preset.ServiceType {
				option.Checked = true
			}
		}
		options = append(options, option)
	}
	return options
}

// Dollars formats cents exactly as dollars, such as "$1234.50" or "-$0.05".
func Dollars(cents int64) string {
	sign := ""
	magnitude := uint64(cents)
	if cents < 0 {
		sign = "-"
		magnitude = -magnitude
	}
	return fmt.Sprintf("%s$%d.%02d", sign, magnitude/100, magnitude%100)
}

// New renders pages that link static files by their plain /static/ path.
func New() (*Renderer, error) {
	return NewWithAssets(func(name string) string { return "/static/" + name })
}

// NewWithAssets renders pages that link each static file, such as
// "styles.css", at assetURL(name).
func NewWithAssets(assetURL func(name string) string) (*Renderer, error) {
	r := &Renderer{pages: map[string]*template.Template{}}
	names := []string{"login", "dashboard", "vehicle-form", "vehicle", "record-form", "record", "reminders", "records", "archived", "odometer", "history", "history-print"}
	for _, n := range names {
		t, e := template.New("base.html").Funcs(template.FuncMap{"date": func(t time.Time) string { return t.Format("Jan 2, 2006") }, "iso": func(t time.Time) string { return t.Format("2006-01-02") }, "money": func(v *int64) string {
			if v == nil {
				return "—"
			}
			return Dollars(*v)
		}, "cents": Dollars, "num": func(v *int64) string {
			if v == nil {
				return "—"
			}
			return fmt.Sprintf("%d", *v)
		}, "checked": func(v bool) template.HTMLAttr {
			if v {
				return template.HTMLAttr("checked")
			}
			return template.HTMLAttr("")
		}, "selected": func(a, b any) template.HTMLAttr {
			if fmt.Sprint(a) == fmt.Sprint(b) {
				return template.HTMLAttr("selected")
			}
			return template.HTMLAttr("")
		}, "query": func(v url.Values) template.URL { return template.URL(v.Encode()) }, "asset": assetURL, "lower": strings.ToLower, "lookupVIN": nhtsa.LookupVIN, "odometerForm": func(v model.Vehicle, ret string) OdometerForm {
			return OdometerForm{Vehicle: v, Return: ret}
		}}).ParseFS(files, "templates/base.html", "templates/partials.html", "templates/"+n+".html")
		if e != nil {
			return nil, e
		}
		r.pages[n] = t
	}
	return r, nil
}
func (r *Renderer) Render(w io.Writer, page string, data any) error {
	t := r.pages[page]
	if t == nil {
		return fmt.Errorf("unknown page %s", page)
	}
	return t.ExecuteTemplate(w, "base", data)
}
func (r *Renderer) RenderNamed(w io.Writer, page, name string, data any) error {
	t := r.pages[page]
	if t == nil {
		return fmt.Errorf("unknown page %s", page)
	}
	return t.ExecuteTemplate(w, name, data)
}
