package nhtsa

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestModelCandidates(t *testing.T) {
	products := []string{"E 350", "E 450 4MATIC", "e350", "EQE 350", "E-CLASS WAGON", "GLC 300", "E 63 AMG S"}
	for _, tt := range []struct {
		name, model, trim string
		products          []string
		want              []string
	}{
		{"family name matches trims but not EQ models", "E-Class", "", products,
			[]string{"E-Class", "E 350", "E 450 4MATIC", "e350", "E-CLASS WAGON", "E 63 AMG S"}},
		{"trim and its spacing variants", "E-Class", "E450 4MATIC", nil,
			[]string{"E-Class", "E450 4MATIC", "E 450 4MATIC", "E450", "E 450"}},
		{"spaced trim is compacted", "E-Class", "E 450", nil,
			[]string{"E-Class", "E 450", "E450"}},
		{"exact model needs no variants", "Explorer", "", []string{"EXPLORER"}, []string{"Explorer"}},
		{"series suffix", "3-Series", "", []string{"330i", "X3", "M3", "340i xDrive"}, []string{"3-Series", "330i", "340i xDrive"}},
		{"first word as family", "GLC", "", []string{"GLC Coupe", "GLC 300", "GLCC"}, []string{"GLC", "GLC Coupe", "GLC 300"}},
		{"blank model and trim", "", " ", nil, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got, _ := ModelCandidates(tt.model, tt.trim, tt.products); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ModelCandidates = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestModelCandidatesAreBounded(t *testing.T) {
	var products []string
	for i := range 40 {
		products = append(products, "X "+strings.Repeat("9", i+1))
	}
	got, truncated := ModelCandidates("X", "", products)
	if len(got) != maxCandidates || !truncated {
		t.Fatalf("%d candidates (truncated=%v), want %d and truncated", len(got), truncated, maxCandidates)
	}
}

// nhtsaFixture serves recorded-shape NHTSA responses so no test calls the
// live API: product model names per make and year, and campaigns per model.
type nhtsaFixture struct {
	products  map[string][]string // "make|year" -> NHTSA model names
	campaigns map[string][]string // "make|model|year" -> campaign numbers
	mu        sync.Mutex
	queried   []string
}

func (f *nhtsaFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	switch r.URL.Path {
	case "/products/vehicle/models":
		names, ok := f.products[strings.ToLower(query.Get("make"))+"|"+query.Get("modelYear")]
		if !ok || query.Get("issueType") != "r" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"Message":"Invalid parameters"}`))
			return
		}
		var results []string
		for _, name := range names {
			results = append(results, `{"model":"`+name+`"}`)
		}
		_, _ = w.Write([]byte(`{"count":` + strconv.Itoa(len(names)) + `,"message":"Results returned successfully","results":[` + strings.Join(results, ",") + `]}`))
	case "/recalls/recallsByVehicle":
		f.mu.Lock()
		f.queried = append(f.queried, query.Get("model"))
		f.mu.Unlock()
		campaigns := f.campaigns[strings.ToLower(query.Get("make"))+"|"+strings.ToLower(query.Get("model"))+"|"+query.Get("modelYear")]
		if len(campaigns) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"Count":0,"Message":"Results returned successfully","results":[]}`))
			return
		}
		var results []string
		for _, number := range campaigns {
			results = append(results, `{"NHTSACampaignNumber":"`+number+`","Component":"C `+number+`","ReportReceivedDate":"01/03/2026"}`)
		}
		_, _ = w.Write([]byte(`{"Count":1,"Message":"Results returned successfully","results":[` + strings.Join(results, ",") + `]}`))
	default:
		http.NotFound(w, r)
	}
}

func campaignNumbers(recalls []Recall) []string {
	var numbers []string
	for _, recall := range recalls {
		numbers = append(numbers, recall.CampaignNumber)
	}
	return numbers
}

func TestRecallsForVehicleFindsFamilyNameRecalls(t *testing.T) {
	fixture := &nhtsaFixture{
		products: map[string][]string{
			"mercedes-benz|2024": {"E 350", "E 450 4MATIC", "EQE 350", "GLC 300"},
			"ford|2020":          {"EXPLORER", "ESCAPE"},
			"honda|2018":         {"ACCORD", "CIVIC"},
		},
		campaigns: map[string][]string{
			// NHTSA files the E-Class campaigns under trim names, not "E-Class".
			"mercedes-benz|e 350|2024":        {"24V224000"},
			"mercedes-benz|e450|2024":         {"24V224000"},
			"mercedes-benz|e 450 4matic|2024": {"26V281000"},
			"mercedes-benz|eqe 350|2024":      {"24V999000"},
			"ford|explorer|2020":              {"20V582000"},
			"honda|accord|2018":               {"18V268000"},
		},
	}
	client := testClient(t, fixture.ServeHTTP)

	got, err := client.RecallsForVehicle(context.Background(), "Mercedes-Benz", "E-Class", "", 2024)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"24V224000", "26V281000"}; !reflect.DeepEqual(campaignNumbers(got), want) {
		t.Fatalf("E-Class campaigns = %v, want %v", campaignNumbers(got), want)
	}
	if slices.Contains(fixture.queried, "EQE 350") || !slices.Contains(fixture.queried, "E-Class") {
		t.Fatalf("queried models = %q", fixture.queried)
	}
	// Without the product list the VIN trim alone still finds them.
	delete(fixture.products, "mercedes-benz|2024")
	if got, err = client.RecallsForVehicle(context.Background(), "Mercedes-Benz", "E-Class", "E450 4MATIC", 2024); err != nil || len(got) != 2 {
		t.Fatalf("trim lookup = %v err=%v", campaignNumbers(got), err)
	}

	// Vehicles whose model name already matches keep working.
	for _, tt := range []struct {
		make, model, want string
		year              int
	}{
		{"Ford", "Explorer", "20V582000", 2020},
		{"Honda", "Accord", "18V268000", 2018},
	} {
		fixture.queried = nil
		got, err = client.RecallsForVehicle(context.Background(), tt.make, tt.model, "", tt.year)
		if err != nil || !reflect.DeepEqual(campaignNumbers(got), []string{tt.want}) {
			t.Fatalf("%s %s = %v err=%v", tt.make, tt.model, campaignNumbers(got), err)
		}
		if !reflect.DeepEqual(fixture.queried, []string{tt.model}) {
			t.Fatalf("%s %s queried %q", tt.make, tt.model, fixture.queried)
		}
	}
}

func TestRecallsForVehicleIsEmptyOnlyAfterEveryNameMisses(t *testing.T) {
	fixture := &nhtsaFixture{products: map[string][]string{"mercedes-benz|2027": {"E 450 4MATIC"}}}
	client := testClient(t, fixture.ServeHTTP)
	got, err := client.RecallsForVehicle(context.Background(), "Mercedes-Benz", "E-Class", "E450 4MATIC", 2027)
	if err != nil || len(got) != 0 {
		t.Fatalf("recalls = %v err=%v", campaignNumbers(got), err)
	}
	if want := []string{"E-Class", "E450 4MATIC", "E 450 4MATIC", "E450", "E 450"}; !reflect.DeepEqual(fixture.queried, want) {
		t.Fatalf("queried %q, want %q", fixture.queried, want)
	}
	// An unknown make has no product list; the stored model is still tried.
	fixture.queried = nil
	if got, err = client.RecallsForVehicle(context.Background(), "Custom", "Thing", "", 2027); err != nil || len(got) != 0 || !reflect.DeepEqual(fixture.queried, []string{"Thing"}) {
		t.Fatalf("unknown make: %v err=%v queried=%q", campaignNumbers(got), err, fixture.queried)
	}
}

func TestRecallsForVehicleDoesNotReportATruncatedLookupAsClean(t *testing.T) {
	var products []string
	for i := range maxCandidates + 3 {
		products = append(products, "E "+strings.Repeat("1", i+1))
	}
	last := products[len(products)-1]
	fixture := &nhtsaFixture{
		products:  map[string][]string{"mercedes-benz|2024": products},
		campaigns: map[string][]string{"mercedes-benz|" + strings.ToLower(last) + "|2024": {"24V000001"}},
	}
	client := testClient(t, fixture.ServeHTTP)
	// Names beyond the limit were skipped and nothing was found: not "no recalls".
	if _, err := client.RecallsForVehicle(context.Background(), "Mercedes-Benz", "E-Class", "", 2024); err == nil || !strings.Contains(err.Error(), "not all checked") {
		t.Fatalf("truncated empty lookup err = %v", err)
	}
	if len(fixture.queried) != maxCandidates || slices.Contains(fixture.queried, last) {
		t.Fatalf("queried %d names, including the last: %v", len(fixture.queried), slices.Contains(fixture.queried, last))
	}
	// Campaigns that were found are still reported.
	fixture.campaigns["mercedes-benz|e-class|2024"] = []string{"24V000002"}
	got, err := client.RecallsForVehicle(context.Background(), "Mercedes-Benz", "E-Class", "", 2024)
	if err != nil || !reflect.DeepEqual(campaignNumbers(got), []string{"24V000002"}) {
		t.Fatalf("partial lookup = %v err=%v", campaignNumbers(got), err)
	}
}

func TestRecallsForVehicleReportsFailures(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/products/") {
			_, _ = w.Write([]byte(`{"results":[{"model":"E 350"}]}`))
			return
		}
		if r.URL.Query().Get("model") == "E 350" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"Message":"Results returned successfully","results":[]}`))
	})
	// A failed name must not read as "no recalls": the check is retried instead.
	if _, err := client.RecallsForVehicle(context.Background(), "Mercedes-Benz", "E-Class", "", 2024); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("error = %v", err)
	}
	down := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	if _, err := down.RecallsForVehicle(context.Background(), "Mercedes-Benz", "E-Class", "", 2024); err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("product lookup error = %v", err)
	}
}

func TestProductModelsQuery(t *testing.T) {
	var got url.Values
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_, _ = w.Write([]byte(`{"results":[{"model":" E 350 "},{"model":""}]}`))
	})
	names, err := client.productModels(context.Background(), " Mercedes-Benz ", 2024)
	if err != nil || !reflect.DeepEqual(names, []string{"E 350"}) {
		t.Fatalf("names = %q err=%v", names, err)
	}
	if got.Get("make") != "Mercedes-Benz" || got.Get("modelYear") != "2024" || got.Get("issueType") != "r" {
		t.Fatalf("query = %v", got)
	}
}
