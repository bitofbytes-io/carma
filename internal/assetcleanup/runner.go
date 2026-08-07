package assetcleanup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/bitofbytes-io/carma/internal/assets"
	"github.com/google/uuid"
)

const GracePeriod = 48 * time.Hour

const (
	TriggerStartup   = "startup"
	TriggerScheduled = "scheduled"
)

type Repository interface {
	ListReferencedAssetKeys(context.Context) ([]string, error)
	TryAssetCleanupLock(context.Context) (unlock func(context.Context) error, acquired bool, err error)
}

type AssetPruner interface {
	Prune(context.Context, map[string]struct{}, time.Time) (assets.CleanupReport, error)
}

type Runner struct {
	repository Repository
	pruner     AssetPruner
	logger     *slog.Logger
	now        func() time.Time
	newRunID   func() string
}

func NewRunner(repository Repository, pruner AssetPruner, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{repository: repository, pruner: pruner, logger: logger, now: time.Now, newRunID: uuid.NewString}
}

func (r *Runner) Run(ctx context.Context, trigger string) (report assets.CleanupReport, runErr error) {
	started := r.now().UTC()
	runID := r.newRunID()
	cutoff := started.Add(-GracePeriod)
	r.logger.Info("asset cleanup started",
		"run_id", runID, "trigger", trigger, "cutoff_time", cutoff, "grace_period", GracePeriod)

	unlock, acquired, err := r.repository.TryAssetCleanupLock(ctx)
	if err != nil {
		r.logFailed(runID, trigger, started, 0, report, err)
		return report, err
	}
	if !acquired {
		r.logger.Info("asset cleanup skipped", "run_id", runID, "trigger", trigger,
			"duration", r.now().UTC().Sub(started), "reason", "lock_contended")
		return report, nil
	}

	keys, err := r.repository.ListReferencedAssetKeys(ctx)
	if err != nil {
		err = fmt.Errorf("list referenced asset keys: %w", err)
	}
	referenced := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		referenced[key] = struct{}{}
	}
	if err == nil {
		report, err = r.pruner.Prune(ctx, referenced, cutoff)
		for _, failure := range report.Failures {
			attributes := []any{"run_id", runID, "path_category", failure.Category, "error", failure.Err}
			if failure.Category == "object" {
				attributes = append(attributes, "storage_key", failure.Key)
			}
			r.logger.Warn("asset cleanup deletion failed", attributes...)
		}
	}
	unlockContext, cancelUnlock := context.WithTimeout(context.Background(), 5*time.Second)
	unlockErr := unlock(unlockContext)
	cancelUnlock()
	runErr = errors.Join(err, unlockErr)
	if runErr != nil {
		r.logFailed(runID, trigger, started, len(referenced), report, runErr)
		return report, runErr
	}
	r.logger.Info("asset cleanup completed", r.terminalAttributes(runID, trigger, started, len(referenced), report)...)
	return report, nil
}

func (r *Runner) logFailed(runID, trigger string, started time.Time, referencedCount int, report assets.CleanupReport, err error) {
	attributes := r.terminalAttributes(runID, trigger, started, referencedCount, report)
	attributes = append(attributes, "error", err)
	r.logger.Error("asset cleanup failed", attributes...)
}

func (r *Runner) terminalAttributes(runID, trigger string, started time.Time, referencedCount int, report assets.CleanupReport) []any {
	return []any{
		"run_id", runID,
		"trigger", trigger,
		"duration", r.now().UTC().Sub(started),
		"referenced_key_count", referencedCount,
		"object_files_scanned", report.ObjectFilesScanned,
		"temporary_files_scanned", report.TemporaryFilesScanned,
		"referenced_files_retained", report.ReferencedFilesRetained,
		"fresh_unreferenced_files_retained", report.FreshUnreferencedFilesRetained,
		"orphan_objects_pruned", report.OrphanObjectsPruned,
		"stale_temporary_files_pruned", report.StaleTemporaryFilesPruned,
		"unknown_files_skipped", report.UnknownFilesSkipped,
		"symlinks_skipped", report.SymlinksSkipped,
		"deletion_failures", report.DeletionFailures,
		"bytes_reclaimed", report.BytesReclaimed,
	}
}
