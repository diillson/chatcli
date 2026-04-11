package workers

// ConcurrencyClass describes how a tool call can be executed concurrently.
type ConcurrencyClass int

const (
	// ConcurrencySerial means the tool must be executed sequentially.
	ConcurrencySerial ConcurrencyClass = iota

	// ConcurrencySafe means the tool can safely run in parallel with other safe tools.
	ConcurrencySafe

	// ConcurrencyFileScoped means the tool can run in parallel as long as no other
	// tool targets the same file path.
	ConcurrencyFileScoped
)

// ClassifyToolConcurrency determines the concurrency safety of a tool call.
// Read-only commands are always safe. Write commands that target specific files
// can run concurrently with other write commands targeting different files.
// Commands like 'exec' are always serial since they may have arbitrary side effects.
func ClassifyToolConcurrency(subcmd string) ConcurrencyClass {
	switch subcmd {
	// Read-only commands: always safe to parallelize
	case "read", "tree", "search", "git-status", "git-diff", "git-log", "git-changed", "git-branch":
		return ConcurrencySafe

	// File-scoped writes: safe if targeting different files
	case "write", "patch", "rollback":
		return ConcurrencyFileScoped

	// Exec/test/clean: may have side effects, must be serial
	case "exec", "test", "clean":
		return ConcurrencySerial

	default:
		return ConcurrencySerial
	}
}

// CanParallelizeToolCalls determines if a batch of resolved tool calls
// can be executed in parallel, and returns the set that can run concurrently.
//
// Rules:
//  1. All ConcurrencySafe tools can always run in parallel.
//  2. ConcurrencyFileScoped tools can run in parallel if they target different files.
//  3. If ANY tool is ConcurrencySerial, the entire batch runs sequentially.
//  4. ConcurrencyFileScoped tools targeting the same file run sequentially.
func CanParallelizeToolCalls(calls []resolvedToolCall) (canParallel bool, parallelSet []int, serialSet []int) {
	if len(calls) == 0 {
		return false, nil, nil
	}
	if len(calls) == 1 {
		return false, nil, []int{0}
	}

	// Check if any call forces serial execution
	for i, rtc := range calls {
		class := ClassifyToolConcurrency(rtc.Subcmd)
		if class == ConcurrencySerial {
			// Serial tool present — everything runs sequentially
			serial := make([]int, len(calls))
			for j := range serial {
				serial[j] = j
			}
			return false, nil, serial
		}
		_ = i
	}

	// No serial tools — classify into parallel vs serial based on file conflicts
	fileTargets := make(map[string]int) // file → first index targeting it
	parallelSet = make([]int, 0, len(calls))
	serialSet = make([]int, 0)

	for i, rtc := range calls {
		class := ClassifyToolConcurrency(rtc.Subcmd)

		if class == ConcurrencySafe {
			parallelSet = append(parallelSet, i)
			continue
		}

		if class == ConcurrencyFileScoped {
			filePath := extractFilePathFromResolved(rtc)
			if filePath == "" {
				// No file path — treat as safe (tree without dir, etc.)
				parallelSet = append(parallelSet, i)
				continue
			}

			if _, conflict := fileTargets[filePath]; conflict {
				// File conflict — this one must be serial
				serialSet = append(serialSet, i)
			} else {
				fileTargets[filePath] = i
				parallelSet = append(parallelSet, i)
			}
		}
	}

	return len(parallelSet) > 1, parallelSet, serialSet
}
