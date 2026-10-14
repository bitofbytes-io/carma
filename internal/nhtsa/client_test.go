package nhtsa

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &Client{HTTP: server.Client(), VPICURL: server.URL + "/api", RecallsURL: server.URL}
}

func TestDecodeVIN(t *testing.T) {
	var path string
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path + "?" + r.URL.RawQuery
		_, _ = w.Write([]byte(`{"Count":1,"Results":[{"Make":"MERCEDES-BENZ","Model":"GLC","ModelYear":"2019","ErrorCode":"1","ErrorText":"1 - Check Digit (9th position) does not calculate properly"}]}`))
	})
	got, err := client.DecodeVIN(context.Background(), " wdc-0g4kb5kv123456 ")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/api/vehicles/DecodeVinValues/WDC0G4KB5KV123456?format=json" {
		t.Fatalf("path = %s", path)
	}
	if got.Make != "Mercedes-Benz" || got.Model != "GLC" || got.Year == nil || *got.Year != 2019 || !strings.Contains(got.Warning, "Check Digit") {
		t.Fatalf("decoded = %+v", got)
	}
}

func TestDecodeVINRejectsInvalidAndUndecodable(t *testing.T) {
	calls := 0
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"Results":[{"Make":"","Model":"","ModelYear":"","ErrorCode":"6,7"}]}`))
	})
	for _, vin := range []string{"", "SHORT", "1HGCM82633A00435O", "1HGCM82633A0043521"} {
		if _, err := client.DecodeVIN(context.Background(), vin); !errors.Is(err, ErrInvalidVIN) {
			t.Fatalf("DecodeVIN(%q) error = %v", vin, err)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid VINs called the API %d times", calls)
	}
	if _, err := client.DecodeVIN(context.Background(), "1HGCM82633A004352"); !errors.Is(err, ErrNotDecoded) {
		t.Fatalf("undecodable VIN error = %v", err)
	}
}

func TestRecallsParsesCampaignsAndEmptyResults(t *testing.T) {
	var query string
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		if r.URL.Query().Get("model") == "none" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"Count":0,"Message":"Results returned successfully","results":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"Count":3,"Message":"Results returned successfully","results":[
			{"NHTSACampaignNumber":"19V493000","Component":"STRUCTURE","Summary":"Spot welds.","Consequence":"Injury risk.","Remedy":"Repair.","ReportReceivedDate":"26/06/2019","parkIt":true,"parkOutSide":false},
			{"NHTSACampaignNumber":"19V493000","Component":"DUPLICATE"},
			{"NHTSACampaignNumber":"21V587000","Component":"FUEL PUMP","ReportReceivedDate":"bad"}]}`))
	})
	recalls, err := client.Recalls(context.Background(), "Subaru", "Outback", 2019)
	if err != nil {
		t.Fatal(err)
	}
	if query != "make=Subaru&model=Outback&modelYear=2019" {
		t.Fatalf("query = %s", query)
	}
	if len(recalls) != 2 || recalls[0].CampaignNumber != "19V493000" || recalls[0].Component != "STRUCTURE" || !recalls[0].ParkIt || recalls[0].ParkOutside {
		t.Fatalf("recalls = %+v", recalls)
	}
	if recalls[0].ReportReceived == nil || !recalls[0].ReportReceived.Equal(time.Date(2019, time.June, 26, 0, 0, 0, 0, time.UTC)) || recalls[1].ReportReceived != nil {
		t.Fatalf("report dates = %v, %v", recalls[0].ReportReceived, recalls[1].ReportReceived)
	}
	if recalls, err = client.Recalls(context.Background(), "Subaru", "none", 2019); err != nil || len(recalls) != 0 {
		t.Fatalf("empty result: %+v err=%v", recalls, err)
	}
}

func TestRecallsReportsServerErrors(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("model") == "bad" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"Message":"Invalid parameters"}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	if _, err := client.Recalls(context.Background(), "Subaru", "Outback", 2019); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("503 error = %v", err)
	}
	if _, err := client.Recalls(context.Background(), "Subaru", "bad", 2019); err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("400 error = %v", err)
	}
}

func TestDisplayName(t *testing.T) {
	for in, want := range map[string]string{"SUBARU": "Subaru", "BMW": "BMW", "LAND ROVER": "Land Rover", "MERCEDES-BENZ": "Mercedes-Benz", "McLaren": "McLaren"} {
		if got := displayName(in); got != want {
			t.Errorf("displayName(%q) = %q, want %q", in, got, want)
		}
	}
}
