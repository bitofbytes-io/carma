package assets

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var objectShardPattern = regexp.MustCompile(`^[0-9a-f]{2}$`)

type DeletionFailure struct {
	Category string
	Key      string
	Err      error
}

type CleanupReport struct {
	ObjectFilesScanned             int
	TemporaryFilesScanned          int
	ReferencedFilesRetained        int
	FreshUnreferencedFilesRetained int
	OrphanObjectsPruned            int
	StaleTemporaryFilesPruned      int
	UnknownFilesSkipped            int
	SymlinksSkipped                int
	DeletionFailures               int
	BytesReclaimed                 int64
	Failures                       []DeletionFailure
}

func (s *LocalStore) Prune(ctx context.Context, referenced map[string]struct{}, cutoff time.Time) (CleanupReport, error) {
	var report CleanupReport
	var scanErrors []error
	objectsRoot := filepath.Join(s.root, "objects")
	err := filepath.WalkDir(objectsRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if walkErr != nil {
			scanErrors = append(scanErrors, fmt.Errorf("scan object storage: %w", walkErr))
			return nil
		}
		if path == objectsRoot {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			report.SymlinksSkipped++
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			rel, relErr := filepath.Rel(objectsRoot, path)
			if relErr != nil {
				scanErrors = append(scanErrors, fmt.Errorf("classify object directory: %w", relErr))
				return filepath.SkipDir
			}
			if !strings.ContainsRune(rel, os.PathSeparator) && objectShardPattern.MatchString(rel) {
				return nil
			}
			return filepath.SkipDir
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			scanErrors = append(scanErrors, fmt.Errorf("inspect object storage entry: %w", infoErr))
			return nil
		}
		if !info.Mode().IsRegular() {
			report.UnknownFilesSkipped++
			return nil
		}
		rel, relErr := filepath.Rel(objectsRoot, path)
		if relErr != nil {
			scanErrors = append(scanErrors, fmt.Errorf("classify object storage entry: %w", relErr))
			return nil
		}
		key := filepath.ToSlash(rel)
		if !keyPattern.MatchString(key) {
			report.UnknownFilesSkipped++
			return nil
		}
		report.ObjectFilesScanned++
		if _, ok := referenced[key]; ok {
			report.ReferencedFilesRetained++
			return nil
		}
		if !info.ModTime().Before(cutoff) {
			report.FreshUnreferencedFilesRetained++
			return nil
		}
		if removeErr := s.remove(path); removeErr != nil {
			report.DeletionFailures++
			report.Failures = append(report.Failures, DeletionFailure{Category: "object", Key: key, Err: removeErr})
			scanErrors = append(scanErrors, fmt.Errorf("delete object %q: %w", key, removeErr))
			return nil
		}
		report.OrphanObjectsPruned++
		report.BytesReclaimed += info.Size()
		return nil
	})
	if err != nil {
		scanErrors = append(scanErrors, err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return report, errors.Join(append(scanErrors, ctxErr)...)
	}

	temporaryRoot := filepath.Join(s.root, "temporary")
	entries, err := os.ReadDir(temporaryRoot)
	if err != nil {
		scanErrors = append(scanErrors, fmt.Errorf("scan temporary storage: %w", err))
		return report, errors.Join(scanErrors...)
	}
	for _, entry := range entries {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return report, errors.Join(errors.Join(scanErrors...), ctxErr)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			report.SymlinksSkipped++
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			scanErrors = append(scanErrors, fmt.Errorf("inspect temporary storage entry: %w", infoErr))
			continue
		}
		if !info.Mode().IsRegular() {
			report.UnknownFilesSkipped++
			continue
		}
		if !strings.HasPrefix(entry.Name(), "upload-") {
			report.UnknownFilesSkipped++
			continue
		}
		report.TemporaryFilesScanned++
		if !info.ModTime().Before(cutoff) {
			report.FreshUnreferencedFilesRetained++
			continue
		}
		path := filepath.Join(temporaryRoot, entry.Name())
		if removeErr := s.remove(path); removeErr != nil {
			report.DeletionFailures++
			report.Failures = append(report.Failures, DeletionFailure{Category: "temporary", Err: removeErr})
			scanErrors = append(scanErrors, fmt.Errorf("delete temporary upload: %w", removeErr))
			continue
		}
		report.StaleTemporaryFilesPruned++
		report.BytesReclaimed += info.Size()
	}
	return report, errors.Join(scanErrors...)
}
