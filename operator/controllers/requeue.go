/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package controllers

import "time"

// immediateRequeueDelay is the delay used where a reconcile must run again
// as soon as possible: after a finalizer or status write that the next pass
// depends on, or after an optimistic-concurrency conflict. controller-runtime
// deprecated Result.Requeue in favor of RequeueAfter; a short fixed delay
// keeps the "run again now" intent without the deprecated field.
const immediateRequeueDelay = time.Second
