package model

import (
	"errors"
	"math"
	"strconv"
	"testing"
)

func TestParseOdometerMiles(t *testing.T) {
	over := strconv.FormatUint(uint64(math.MaxInt64)+1, 10)
	tests := []struct {
		name    string
		raw     string
		want    int64
		wantErr bool
	}{
		{name: "zero", raw: "0", want: 0},
		{name: "positive", raw: "1200", want: 1200},
		{name: "surrounding space", raw: "  1200\t", want: 1200},
		{name: "leading zeros", raw: "00012", want: 12},
		{name: "explicit plus", raw: "+15", want: 15},
		{name: "negative zero", raw: "-0", want: 0},
		{name: "max int64", raw: strconv.FormatInt(math.MaxInt64, 10), want: math.MaxInt64},
		{name: "empty", raw: "", wantErr: true},
		{name: "space only", raw: "   ", wantErr: true},
		{name: "negative", raw: "-1", wantErr: true},
		{name: "fraction", raw: "1.5", wantErr: true},
		{name: "words", raw: "oops", wantErr: true},
		{name: "thousands separator", raw: "12,345", wantErr: true},
		{name: "internal space", raw: "12 345", wantErr: true},
		{name: "overflow", raw: over, wantErr: true},
		{name: "plus overflow", raw: "+" + over, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseOdometerMiles(test.raw)
			if test.wantErr {
				if !errors.Is(err, ErrInvalidOdometer) || got != 0 {
					t.Fatalf("ParseOdometerMiles(%q)=%d,%v want 0, ErrInvalidOdometer", test.raw, got, err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("ParseOdometerMiles(%q)=%d,%v want %d", test.raw, got, err, test.want)
			}
		})
	}
}
