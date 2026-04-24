/*
 * FileExists — evaluator that checks filesystem state.
 *
 * Spec:
 *   path         string   — required; absolute or relative to workspace
 *   min_size     int      — optional; bytes. File must be at least this big.
 *   require_dir  bool     — optional; must be a directory
 *   stable_for   duration — optional; mtime must not change for N seconds
 *                            (useful for watching downloads complete)
 */
package condition

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/scheduler"
)

// FileExists implements scheduler.ConditionEvaluator.
type FileExists struct{}

// NewFileExists builds the evaluator.
func NewFileExists() *FileExists { return &FileExists{} }

// Type returns the Condition.Type literal.
func (FileExists) Type() string { return "file_exists" }

// ValidateSpec enforces required fields.
func (FileExists) ValidateSpec(spec map[string]any) error {
	if strings.TrimSpace(asString(spec, "path")) == "" {
		return fmt.Errorf("file_exists: spec.path is required")
	}
	return nil
}

// Evaluate checks the filesystem.
func (FileExists) Evaluate(_ context.Context, cond scheduler.Condition, env *scheduler.EvalEnv) scheduler.EvalOutcome {
	path := cond.SpecString("path", "")
	minSize := int64(cond.SpecInt("min_size", 0))
	requireDir := cond.SpecBool("require_dir", false)
	stableFor := cond.SpecDuration("stable_for", 0)

	if !filepath.IsAbs(path) && env != nil && env.Bridge != nil {
		if ws := env.Bridge.WorkspaceDir(); ws != "" {
			path = filepath.Join(ws, path)
		}
	}

	info, err := os.Stat(path) //#nosec G304 -- operator-scheduled path, audited
	if err != nil {
		if os.IsNotExist(err) {
			return scheduler.EvalOutcome{
				Satisfied: false,
				Details:   fmt.Sprintf("%s: not found", path),
			}
		}
		return scheduler.EvalOutcome{Err: err, Details: path}
	}
	if requireDir && !info.IsDir() {
		return scheduler.EvalOutcome{
			Satisfied: false,
			Details:   fmt.Sprintf("%s: not a directory", path),
		}
	}
	if !requireDir && info.IsDir() {
		// A file was expected; treat directory as not-ready.
		return scheduler.EvalOutcome{
			Satisfied: false,
			Details:   fmt.Sprintf("%s: is a directory", path),
		}
	}
	if minSize > 0 && info.Size() < minSize {
		return scheduler.EvalOutcome{
			Satisfied: false,
			Details:   fmt.Sprintf("%s: size %d < min_size %d", path, info.Size(), minSize),
		}
	}
	if stableFor > 0 {
		age := time.Since(info.ModTime())
		if age < stableFor {
			return scheduler.EvalOutcome{
				Satisfied: false,
				Details:   fmt.Sprintf("%s: mtime age %s < stable_for %s", path, age, stableFor),
			}
		}
	}
	return scheduler.EvalOutcome{
		Satisfied: true,
		Details:   fmt.Sprintf("%s: size=%d mtime=%s", path, info.Size(), info.ModTime().Format(time.RFC3339)),
	}
}
