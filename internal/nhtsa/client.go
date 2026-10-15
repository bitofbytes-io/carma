// Package nhtsa calls NHTSA's free, keyless public APIs: vPIC to decode VINs
// and the recalls API to list safety recall campaigns for a model year.
package nhtsa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultVPICURL    = "https://vpic.nhtsa.dot.gov/api"
	DefaultRecallsURL = "https://api.nhtsa.gov"
	requestTimeout    = 15 * time.Second
	maxResponseBytes  = 4 << 20
)

// ErrInvalidVIN reports a VIN that is not 17 VIN characters.
var ErrInvalidVIN = errors.New("a VIN is 17 letters and digits, without I, O, or Q")

// ErrNotDecoded reports a VIN that vPIC could not match to a make and model.
var ErrNotDecoded = errors.New("NHTSA could not decode this VIN")

var vinPattern = regexp.MustCompile(`^[A-HJ-NPR-Z0-9]{17}$`)

type Client struct {
	HTTP       *http.Client
	VPICURL    string
	RecallsURL string
}

func NewClient() *Client {
	return &Client{HTTP: &http.Client{Timeout: requestTimeout}, VPICURL: DefaultVPICURL, RecallsURL: DefaultRecallsURL}
}

// Vehicle is what a VIN decodes to. Warning carries vPIC's note when the VIN
// decoded with errors, such as a bad check digit.
type Vehicle struct {
	Make, Model string
	Year        *int
	Warning     string
}

// Recall is one NHTSA safety recall campaign.
type Recall struct {
	CampaignNumber                          string
	Component, Summary, Consequence, Remedy string
	// ReportReceived is when the manufacturer reported the recall, if known.
	ReportReceived      *time.Time
	ParkIt, ParkOutside bool
}

// NormalizeVIN uppercases vin and strips spaces and dashes.
func NormalizeVIN(vin string) string {
	return strings.NewReplacer(" ", "", "-", "").Replace(strings.ToUpper(strings.TrimSpace(vin)))
}

// LookupVIN returns vin normalized when it is a valid 17-character VIN, or ""
// when it is not.
func LookupVIN(vin string) string {
	if vin = NormalizeVIN(vin); vinPattern.MatchString(vin) {
		return vin
	}
	return ""
}

// DecodeVIN looks up the make, model, and model year for a 17-character VIN.
func (c *Client) DecodeVIN(ctx context.Context, vin string) (Vehicle, error) {
	vin = NormalizeVIN(vin)
	if !vinPattern.MatchString(vin) {
		return Vehicle{}, ErrInvalidVIN
	}
	var body struct {
		Results []struct {
			Make, Model, ModelYear, ErrorCode, ErrorText string
		}
	}
	endpoint := strings.TrimRight(c.VPICURL, "/") + "/vehicles/DecodeVinValues/" + url.PathEscape(vin) + "?format=json"
	if _, err := c.getJSON(ctx, endpoint, &body); err != nil {
		return Vehicle{}, err
	}
	if len(body.Results) == 0 || strings.TrimSpace(body.Results[0].Make) == "" || strings.TrimSpace(body.Results[0].Model) == "" {
		return Vehicle{}, ErrNotDecoded
	}
	result := body.Results[0]
	decoded := Vehicle{Make: displayName(result.Make), Model: strings.TrimSpace(result.Model)}
	if year, err := strconv.Atoi(strings.TrimSpace(result.ModelYear)); err == nil && year >= 1886 && year <= 9999 {
		decoded.Year = &year
	}
	if code := strings.TrimSpace(result.ErrorCode); code != "" && code != "0" {
		decoded.Warning = strings.TrimSpace(result.ErrorText)
	}
	return decoded, nil
}

// Recalls lists the recall campaigns NHTSA has for a make, model, and model
// year. Campaigns cover the model year, so some may not apply to every VIN.
func (c *Client) Recalls(ctx context.Context, vehicleMake, model string, year int) ([]Recall, error) {
	query := url.Values{"make": {strings.TrimSpace(vehicleMake)}, "model": {strings.TrimSpace(model)}, "modelYear": {strconv.Itoa(year)}}
	var body struct {
		Message string
		Results []struct {
			NHTSACampaignNumber                     string
			Component, Summary, Consequence, Remedy string
			ReportReceivedDate                      string
			ParkIt                                  bool `json:"parkIt"`
			ParkOutSide                             bool `json:"parkOutSide"`
		} `json:"results"`
	}
	// The API answers 400 with an empty, successful result for an unknown model.
	status, err := c.getJSON(ctx, strings.TrimRight(c.RecallsURL, "/")+"/recalls/recallsByVehicle?"+query.Encode(), &body)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK && !strings.HasPrefix(body.Message, "Results returned successfully") {
		return nil, fmt.Errorf("NHTSA recalls: HTTP %d", status)
	}
	recalls := make([]Recall, 0, len(body.Results))
	seen := map[string]bool{}
	for _, result := range body.Results {
		campaign := strings.TrimSpace(result.NHTSACampaignNumber)
		if campaign == "" || seen[campaign] {
			continue
		}
		seen[campaign] = true
		recall := Recall{
			CampaignNumber: campaign,
			Component:      strings.TrimSpace(result.Component),
			Summary:        strings.TrimSpace(result.Summary),
			Consequence:    strings.TrimSpace(result.Consequence),
			Remedy:         strings.TrimSpace(result.Remedy),
			ParkIt:         result.ParkIt,
			ParkOutside:    result.ParkOutSide,
		}
		if received, err := time.Parse("02/01/2006", strings.TrimSpace(result.ReportReceivedDate)); err == nil {
			recall.ReportReceived = &received
		}
		recalls = append(recalls, recall)
	}
	return recalls, nil
}

// getJSON decodes a JSON response with status 200 or 400 into target and
// returns the status; the APIs report lookup problems in 400 JSON bodies.
func (c *Client) getJSON(ctx context.Context, endpoint string, target any) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Accept", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("NHTSA request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusBadRequest {
		return response.StatusCode, fmt.Errorf("NHTSA request: HTTP %d", response.StatusCode)
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(target); err != nil {
		return response.StatusCode, fmt.Errorf("NHTSA response: %w", err)
	}
	return response.StatusCode, nil
}

// displayName turns vPIC's uppercase makes such as "SUBARU" or "MERCEDES-BENZ"
// into "Subaru" and "Mercedes-Benz", keeping short acronyms such as "BMW".
func displayName(name string) string {
	words := strings.Fields(name)
	for i, word := range words {
		if len(word) <= 3 || strings.ToUpper(word) != word {
			continue
		}
		runes := []rune(strings.ToLower(word))
		for j := range runes {
			if j == 0 || runes[j-1] == '-' {
				runes[j] = []rune(strings.ToUpper(string(runes[j])))[0]
			}
		}
		words[i] = string(runes)
	}
	return strings.Join(words, " ")
}
