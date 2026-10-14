package export

import (
	"math"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
)

func TestSummarizeTotalsCostsReceiptsAndDates(t *testing.T) {
	empty := Summarize(nil)
	if empty.Records != 0 || empty.Receipts != 0 || empty.TotalCents != nil || empty.First != nil || empty.Last != nil {
		t.Fatalf("empty summary=%+v", empty)
	}
	cost := func(cents int64) *int64 { return &cents }
	day := func(value string) time.Time {
		parsed, _ := time.Parse("2006-01-02", value)
		return parsed
	}
	unpriced := Summarize([]model.Record{{OccurredOn: day("2024-03-01")}})
	if unpriced.TotalCents != nil {
		t.Fatalf("total without costs=%d, want nil", *unpriced.TotalCents)
	}
	summary := Summarize([]model.Record{
		{OccurredOn: day("2025-06-10"), CostCents: cost(8900), AttachmentCount: 1},
		{OccurredOn: day("2023-01-05"), AttachmentCount: 2},
		{OccurredOn: day("2026-02-20"), CostCents: cost(45050)},
	})
	if summary.Records != 3 || summary.Receipts != 3 || summary.TotalCents == nil || *summary.TotalCents != 53950 {
		t.Fatalf("summary=%+v", summary)
	}
	if !summary.First.Equal(day("2023-01-05")) || !summary.Last.Equal(day("2026-02-20")) {
		t.Fatalf("dates first=%v last=%v", summary.First, summary.Last)
	}
}

func TestSummarizeCapsOverflowingTotal(t *testing.T) {
	huge := int64(math.MaxInt64)
	summary := Summarize([]model.Record{{CostCents: &huge}, {CostCents: &huge}})
	if summary.TotalCents == nil || *summary.TotalCents != math.MaxInt64 {
		t.Fatalf("total=%v, want capped at MaxInt64", summary.TotalCents)
	}
}
