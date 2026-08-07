/*
 * ChatCLI - wait_until timeout messaging tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package scheduler

import (
	"strings"
	"testing"
	"time"
)

// TestWaitGaveUpError_IsActionable: the wait_until timeout must teach the
// model how to recover (query_job / async), never a bare deadline error.
func TestWaitGaveUpError_IsActionable(t *testing.T) {
	err := waitGaveUpError(35*time.Minute, JobID("job-42"), StatusRunning)
	for _, want := range []string{"job-42", "KEEPS RUNNING", "query_job", `"async":true`, "35m0s"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("timeout message misses %q: %s", want, err.Error())
		}
	}
}
