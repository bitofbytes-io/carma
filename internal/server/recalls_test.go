package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/nhtsa"
)

type fakeVINDecoder struct {
	vehicle nhtsa.Vehicle
	err     error
	vins    []string
}

func (f *fakeVINDecoder) DecodeVIN(_ context.Context, vin string) (nhtsa.Vehicle, error) {
	f.vins = append(f.vins, vin)
	return f.vehicle, f.err
}

func postVINDecode(t *testing.T, f fixture, fields url.Values) (int, string) {
	t.Helper()
	w := f.do(t, "POST", "/vehicles/vin-decode", strings.NewReader(fields.Encode()), "application/x-www-form-urlencoded")
	return w.Code, w.Body.String()
}

func TestVINDecodeFillsIdentityFields(t *testing.T) {
	f := setup(t)
	if code, _ := postVINDecode(t, f, url.Values{"vin": {"1HGCM82633A004352"}}); code != http.StatusNotFound {
		t.Fatalf("decode while disabled: %d", code)
	}
	if w := f.do(t, "GET", "/vehicles/new", nil, ""); strings.Contains(w.Body.String(), "Decode VIN") {
		t.Fatal("decode button shown while disabled")
	}

	year := 2003
	decoder := &fakeVINDecoder{vehicle: nhtsa.Vehicle{Make: "Honda", Model: "Accord", Year: &year}}
	f.s.EnableVINDecode(decoder)
	if w := f.do(t, "GET", "/vehicles/new", nil, ""); !strings.Contains(w.Body.String(), "Decode VIN") {
		t.Fatal("decode button missing while enabled")
	}
	code, body := postVINDecode(t, f, url.Values{"vin": {" 1hgcm82633a004352 "}, "make": {"typed"}, "license_plate": {"ABC 123"}})
	if code != http.StatusOK || len(decoder.vins) != 1 || decoder.vins[0] != "1hgcm82633a004352" {
		t.Fatalf("decode: %d vins=%v", code, decoder.vins)
	}
	for _, want := range []string{`id="vehicle-identity"`, `name="year" min="1886" max="9999" value="2003"`, `name="make" value="Honda"`, `name="model" value="Accord"`, `value="1HGCM82633A004352"`, `value="ABC 123"`, "Filled in from NHTSA: 2003 Honda Accord."} {
		if !strings.Contains(body, want) {
			t.Fatalf("decoded fields missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "<html") {
		t.Fatal("decode returned a full page")
	}

	decoder.err = nhtsa.ErrNotDecoded
	if _, body = postVINDecode(t, f, url.Values{"vin": {"1HGCM82633A004352"}, "year": {"2010"}, "make": {"Mine"}}); !strings.Contains(body, "could not decode") || !strings.Contains(body, `value="Mine"`) || !strings.Contains(body, `value="2010"`) {
		t.Fatalf("undecodable VIN should keep typed fields: %s", body)
	}
	decoder.err = errors.New("dial tcp: timeout")
	if _, body = postVINDecode(t, f, url.Values{"vin": {"1HGCM82633A004352"}}); !strings.Contains(body, "not responding") || strings.Contains(body, "dial tcp") {
		t.Fatalf("lookup failure message: %s", body)
	}
}

func TestRecallsShowOnDashboardAndCanBeDismissed(t *testing.T) {
	f := setup(t)
	vehicle := createVehicle(t, f)
	if w := f.do(t, "GET", "/vehicles/"+vehicle.ID.String(), nil, ""); strings.Contains(w.Body.String(), `id="recalls"`) {
		t.Fatal("recall section shown without recalls")
	}
	recalls := []model.Recall{{CampaignNumber: "19V493000", Component: "STRUCTURE", Summary: "Spot welds.", ParkIt: true}}
	if _, err := f.store.SaveRecallCheck(t.Context(), vehicle, recalls, f.s.now()); err != nil {
		t.Fatal(err)
	}
	dashboard := f.do(t, "GET", "/", nil, "").Body.String()
	if !strings.Contains(dashboard, "RECALL · DO NOT DRIVE") || !strings.Contains(dashboard, "1 open safety recall") || !strings.Contains(dashboard, `/vehicles/`+vehicle.ID.String()+`#recalls`) {
		t.Fatalf("dashboard recall row missing: %s", dashboard)
	}
	page := f.do(t, "GET", "/vehicles/"+vehicle.ID.String(), nil, "").Body.String()
	if !strings.Contains(page, `id="recalls"`) || !strings.Contains(page, "Spot welds.") {
		t.Fatalf("vehicle recall section missing: %s", page)
	}

	base := "/vehicles/" + vehicle.ID.String() + "/recalls/"
	if w := f.do(t, "POST", base+"00V000000/dismiss", nil, ""); w.Code != http.StatusNotFound {
		t.Fatalf("dismiss unknown campaign: %d", w.Code)
	}
	w := f.do(t, "POST", base+"19V493000/dismiss", nil, "")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/vehicles/"+vehicle.ID.String()+"#recalls" {
		t.Fatalf("dismiss: %d %s", w.Code, w.Header().Get("Location"))
	}
	if dashboard = f.do(t, "GET", "/", nil, "").Body.String(); strings.Contains(dashboard, "RECALL") {
		t.Fatal("dismissed recall still on the dashboard")
	}
	if page = f.do(t, "GET", "/vehicles/"+vehicle.ID.String(), nil, "").Body.String(); !strings.Contains(page, "Dismissed (1)") || !strings.Contains(page, "No open recalls.") {
		t.Fatalf("dismissed recall not listed: %s", page)
	}
	if w = f.do(t, "POST", base+"19V493000/restore", nil, ""); w.Code != http.StatusSeeOther {
		t.Fatalf("restore: %d", w.Code)
	}
	if dashboard = f.do(t, "GET", "/", nil, "").Body.String(); !strings.Contains(dashboard, "RECALL") {
		t.Fatal("restored recall missing from the dashboard")
	}
}
