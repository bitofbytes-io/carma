package main

import (
	"testing"

	"github.com/google/uuid"
)

func TestParseOptionsDryRunAndTarget(t *testing.T) {
	id := uuid.New()
	options, err := parseOptions([]string{"--dry-run", "--reminder-id", id.String(), "--require-recipient-count", "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !options.DryRun || options.ReminderID == nil || *options.ReminderID != id || options.RequireRecipientCount == nil || *options.RequireRecipientCount != 1 {
		t.Fatalf("options = %+v", options)
	}
}

func TestParseOptionsRejectsInvalidInput(t *testing.T) {
	for _, args := range [][]string{{"--reminder-id", "nope"}, {"--require-recipient-count", "0", "--reminder-id", uuid.NewString()}, {"--require-recipient-count", "1"}, {"positional"}, {"--unknown"}} {
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("accepted args %#v", args)
		}
	}
}
