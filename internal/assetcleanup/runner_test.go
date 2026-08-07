package assetcleanup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/assets"
)

type fakeRepository struct {
	keys             []string
	lockErr, keysErr error
	unlockErr        error
	acquired         bool
	keysCalled       bool
}

func (f *fakeRepository) TryAssetCleanupLock(context.Context) (func(context.Context) error, bool, error) {
	return func(context.Context) error { return f.unlockErr }, f.acquired, f.lockErr
}

func (f *fakeRepository) ListReferencedAssetKeys(context.Context) ([]string, error) {
	f.keysCalled = true
	return f.keys, f.keysErr
}

type fakePruner struct {
	report assets.CleanupReport
	err    error
	called bool
	keys   map[string]struct{}
	cutoff time.Time
}

func (f *fakePruner) Prune(_ context.Context, keys map[string]struct{}, cutoff time.Time) (assets.CleanupReport, error) {
	f.called, f.keys, f.cutoff = true, keys, cutoff
	return f.report, f.err
}

func testRunner(repository Repository, pruner AssetPruner) (*Runner, *bytes.Buffer) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	runner := NewRunner(repository, pruner, logger)
	runner.now = func() time.Time { return time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC) }
	runner.newRunID = func() string { return "run-123" }
	return runner, &output
}

func logEvents(t *testing.T, output *bytes.Buffer) []map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	events := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode log %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func TestRunnerLogsSuccessfulRunWithAllCounters(t *testing.T) {
	repository := &fakeRepository{acquired: true, keys: []string{"aa/key.pdf", "bb/key.jpg", "aa/key.pdf"}}
	expected := assets.CleanupReport{
		ObjectFilesScanned: 9, TemporaryFilesScanned: 2, ReferencedFilesRetained: 3,
		FreshUnreferencedFilesRetained: 4, OrphanObjectsPruned: 2, StaleTemporaryFilesPruned: 1,
		UnknownFilesSkipped: 5, SymlinksSkipped: 2, BytesReclaimed: 1234,
	}
	pruner := &fakePruner{report: expected}
	runner, output := testRunner(repository, pruner)

	report, err := runner.Run(t.Context(), TriggerStartup)
	if err != nil || !reflect.DeepEqual(report, expected) {
		t.Fatalf("report=%+v error=%v", report, err)
	}
	if !pruner.called || len(pruner.keys) != 2 || !pruner.cutoff.Equal(time.Date(2026, time.August, 4, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("pruner called=%t keys=%v cutoff=%v", pruner.called, pruner.keys, pruner.cutoff)
	}
	events := logEvents(t, output)
	if len(events) != 2 || events[0]["msg"] != "asset cleanup started" || events[1]["msg"] != "asset cleanup completed" {
		t.Fatalf("events=%v", events)
	}
	start := events[0]
	if start["run_id"] != "run-123" || start["trigger"] != TriggerStartup || start["grace_period"] == nil || start["cutoff_time"] == nil {
		t.Fatalf("start event=%v", start)
	}
	complete := events[1]
	for key, want := range map[string]float64{
		"referenced_key_count": 2, "object_files_scanned": 9, "temporary_files_scanned": 2,
		"referenced_files_retained": 3, "fresh_unreferenced_files_retained": 4,
		"orphan_objects_pruned": 2, "stale_temporary_files_pruned": 1,
		"unknown_files_skipped": 5, "symlinks_skipped": 2, "deletion_failures": 0,
		"bytes_reclaimed": 1234,
	} {
		if complete[key] != want {
			t.Errorf("%s=%v, want %v (event=%v)", key, complete[key], want, complete)
		}
	}
}

func TestRunnerReferenceFailureIsFailClosedAndLogged(t *testing.T) {
	repository := &fakeRepository{acquired: true, keysErr: errors.New("reference query failed")}
	pruner := &fakePruner{}
	runner, output := testRunner(repository, pruner)

	_, err := runner.Run(t.Context(), TriggerScheduled)
	if err == nil || pruner.called {
		t.Fatalf("error=%v pruner called=%t", err, pruner.called)
	}
	events := logEvents(t, output)
	if len(events) != 2 || events[0]["msg"] != "asset cleanup started" || events[1]["msg"] != "asset cleanup failed" || events[1]["run_id"] != "run-123" {
		t.Fatalf("events=%v", events)
	}
}

func TestRunnerLockContentionSkipsReferenceLoadingAndScanning(t *testing.T) {
	repository := &fakeRepository{}
	pruner := &fakePruner{}
	runner, output := testRunner(repository, pruner)

	if _, err := runner.Run(t.Context(), TriggerStartup); err != nil {
		t.Fatal(err)
	}
	if repository.keysCalled || pruner.called {
		t.Fatalf("keys called=%t prune called=%t", repository.keysCalled, pruner.called)
	}
	events := logEvents(t, output)
	if len(events) != 2 || events[1]["msg"] != "asset cleanup skipped" || events[1]["reason"] != "lock_contended" {
		t.Fatalf("events=%v", events)
	}
}

func TestRunnerLockErrorProducesFailedTerminal(t *testing.T) {
	repository := &fakeRepository{lockErr: errors.New("lock database unavailable")}
	runner, output := testRunner(repository, &fakePruner{})

	if _, err := runner.Run(t.Context(), TriggerStartup); err == nil {
		t.Fatal("lock failure unexpectedly succeeded")
	}
	events := logEvents(t, output)
	if len(events) != 2 || events[0]["msg"] != "asset cleanup started" || events[1]["msg"] != "asset cleanup failed" {
		t.Fatalf("events=%v", events)
	}
}

func TestRunnerUnlockFailureProducesFailedTerminalWithCounts(t *testing.T) {
	repository := &fakeRepository{acquired: true, unlockErr: errors.New("unlock failed")}
	pruner := &fakePruner{report: assets.CleanupReport{OrphanObjectsPruned: 1, BytesReclaimed: 88}}
	runner, output := testRunner(repository, pruner)

	report, err := runner.Run(t.Context(), TriggerScheduled)
	if err == nil || report.OrphanObjectsPruned != 1 {
		t.Fatalf("report=%+v error=%v", report, err)
	}
	events := logEvents(t, output)
	if len(events) != 2 || events[1]["msg"] != "asset cleanup failed" || events[1]["orphan_objects_pruned"] != float64(1) || events[1]["bytes_reclaimed"] != float64(88) {
		t.Fatalf("events=%v", events)
	}
}

func TestRunnerPartialDeletionFailureRetainsCountsAndProtectsFilenames(t *testing.T) {
	repository := &fakeRepository{acquired: true, keys: []string{"private-receipt-name.pdf"}}
	pruneErr := errors.New("NFS remove failed")
	pruner := &fakePruner{report: assets.CleanupReport{
		ObjectFilesScanned: 3, OrphanObjectsPruned: 2, DeletionFailures: 1, BytesReclaimed: 777,
		Failures: []assets.DeletionFailure{{Category: "object", Key: "bb/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb.pdf", Err: pruneErr}},
	}, err: pruneErr}
	runner, output := testRunner(repository, pruner)

	report, err := runner.Run(t.Context(), TriggerScheduled)
	if err == nil || report.OrphanObjectsPruned != 2 || report.BytesReclaimed != 777 {
		t.Fatalf("report=%+v error=%v", report, err)
	}
	events := logEvents(t, output)
	if len(events) != 3 || events[1]["msg"] != "asset cleanup deletion failed" || events[2]["msg"] != "asset cleanup failed" {
		t.Fatalf("events=%v", events)
	}
	if events[2]["orphan_objects_pruned"] != float64(2) || events[2]["deletion_failures"] != float64(1) || events[2]["bytes_reclaimed"] != float64(777) {
		t.Fatalf("failed event counters=%v", events[2])
	}
	if strings.Contains(output.String(), "private-receipt-name.pdf") {
		t.Fatalf("original filename leaked: %s", output.String())
	}
	if events[1]["storage_key"] != "bb/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb.pdf" || events[1]["path_category"] != "object" {
		t.Fatalf("warning event=%v", events[1])
	}
}

func TestDefaultScheduleIntervalIsWeekly(t *testing.T) {
	if DefaultInterval != 7*24*time.Hour {
		t.Fatalf("default interval = %v", DefaultInterval)
	}
}

func TestScheduleRunsAtStartupRepeatsAndStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var mu sync.Mutex
	var triggers []string
	called := make(chan struct{}, 4)
	run := func(_ context.Context, stringTrigger string) (assets.CleanupReport, error) {
		mu.Lock()
		triggers = append(triggers, stringTrigger)
		mu.Unlock()
		select {
		case called <- struct{}{}:
		default:
		}
		return assets.CleanupReport{}, nil
	}
	done := make(chan struct{})
	go func() {
		Schedule(ctx, 10*time.Millisecond, run)
		close(done)
	}()
	for range 2 {
		select {
		case <-called:
		case <-time.After(time.Second):
			t.Fatal("scheduled run did not occur")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(triggers) < 2 || triggers[0] != TriggerStartup || triggers[1] != TriggerScheduled {
		t.Fatalf("triggers=%v", triggers)
	}
}
