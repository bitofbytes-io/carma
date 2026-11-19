package ui

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestBaseTemplateLinksStaticFilesThroughAssetURLs(t *testing.T) {
	r, err := NewWithAssets(func(name string) string { return "/static/" + name + "?v=test" })
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	type data struct {
		Title, Error, Flash, Redirect string
		Authenticated, Development    bool
	}
	if err := r.Render(&b, "login", data{}); err != nil {
		t.Fatal(err)
	}
	body := b.String()
	for _, name := range []string{"favicon.svg", "styles.css", "reminders.css", "htmx.min.js", "app.js"} {
		if !strings.Contains(body, `="/static/`+name+`?v=test"`) {
			t.Errorf("base template does not link %s by its asset URL: %s", name, body)
		}
		if _, err := os.Stat(filepath.Join("..", "..", "static", name)); err != nil {
			t.Errorf("linked static file: %v", err)
		}
	}
	if links := strings.Count(body, `"/static/`); links != 5 {
		t.Errorf("base template links %d static files, want 5 versioned links", links)
	}
}

func TestLoginTemplateRendersAuthenticationVariantsWithoutInviteOnlyFooter(t *testing.T) {
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
	tests := []struct {
		name      string
		data      data
		want      []string
		doNotWant []string
	}{
		{
			name: "Google sign-in",
			data: data{
				Error:    "This Google account is not authorized for Carma.",
				Redirect: "/vehicles/123?sort=cost&direction=desc",
			},
			want: []string{
				`<div class="error" role="alert">This Google account is not authorized for Carma.</div>`,
				`<a class="primary wide center" href="/api/auth/google?redirect=%2Fvehicles%2F123%3Fsort%3Dcost%26direction%3Ddesc">Sign in with Google</a>`,
			},
			doNotWant: []string{
				`<form method="post" action="/login">`,
				"Continue as local developer",
			},
		},
		{
			name: "development login",
			data: data{Development: true},
			want: []string{
				`<form method="post" action="/login"><button class="primary wide">Continue as local developer</button></form>`,
			},
			doNotWant: []string{
				"Sign in with Google",
				`/api/auth/google`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			if err := r.Render(&b, "login", tt.data); err != nil {
				t.Fatal(err)
			}
			body := b.String()
			for _, want := range tt.want {
				if !strings.Contains(body, want) {
					t.Errorf("missing login markup %q in %s", want, body)
				}
			}
			for _, unwanted := range append(tt.doNotWant, "Access is invite-only.", "<small>") {
				if strings.Contains(body, unwanted) {
					t.Errorf("unexpected login markup %q in %s", unwanted, body)
				}
			}
		})
	}
}

func TestDollarsFormatsCentsExactly(t *testing.T) {
	for cents, want := range map[int64]string{
		0:                "$0.00",
		5:                "$0.05",
		123450:           "$1234.50",
		-5:               "-$0.05",
		9007199254740993: "$90071992547409.93",
		math.MaxInt64:    "$92233720368547758.07",
		math.MinInt64:    "-$92233720368547758.08",
	} {
		if got := Dollars(cents); got != want {
			t.Errorf("Dollars(%d)=%q want %q", cents, got, want)
		}
	}
}
