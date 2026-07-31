package ui

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"net/url"
	"strings"
	"time"
)

//go:embed templates/*.html
var files embed.FS

type Renderer struct{ pages map[string]*template.Template }

func New() (*Renderer, error) {
	r := &Renderer{pages: map[string]*template.Template{}}
	names := []string{"login", "dashboard", "vehicle-form", "vehicle", "record-form", "record", "reminders", "records"}
	for _, n := range names {
		t, e := template.New("base.html").Funcs(template.FuncMap{"date": func(t time.Time) string { return t.Format("Jan 2, 2006") }, "iso": func(t time.Time) string { return t.Format("2006-01-02") }, "money": func(v *int64) string {
			if v == nil {
				return "—"
			}
			return fmt.Sprintf("$%.2f", float64(*v)/100)
		}, "num": func(v *int64) string {
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
		}, "query": func(v url.Values) template.URL { return template.URL(v.Encode()) }, "lower": strings.ToLower}).ParseFS(files, "templates/base.html", "templates/"+n+".html")
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
