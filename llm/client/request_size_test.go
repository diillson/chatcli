package client

import (
	"errors"
	"fmt"
	"testing"
)

func TestWithRequestSize_AnnotatesAndExtracts(t *testing.T) {
	base := errors.New("StatusCode: 403, deserialization failed, invalid character '<' looking for beginning of value")
	wrapped := WithRequestSize(base, 129470)

	if wrapped.Error() != base.Error() {
		t.Errorf("Error() must be a passthrough: got %q, want %q", wrapped.Error(), base.Error())
	}
	if !errors.Is(wrapped, base) {
		t.Error("errors.Is must still match the inner error through the wrapper")
	}
	size, ok := RequestSizeFromError(wrapped)
	if !ok || size != 129470 {
		t.Errorf("RequestSizeFromError = (%d, %v), want (129470, true)", size, ok)
	}
}

func TestWithRequestSize_SurvivesOuterWrapping(t *testing.T) {
	base := errors.New("blocked")
	wrapped := fmt.Errorf("turn 2 failed: %w", WithRequestSize(base, 4096))

	size, ok := RequestSizeFromError(wrapped)
	if !ok || size != 4096 {
		t.Errorf("RequestSizeFromError through fmt.Errorf = (%d, %v), want (4096, true)", size, ok)
	}
}

func TestWithRequestSize_NilAndNonPositive(t *testing.T) {
	if got := WithRequestSize(nil, 100); got != nil {
		t.Errorf("WithRequestSize(nil, 100) = %v, want nil", got)
	}
	base := errors.New("x")
	if _, ok := RequestSizeFromError(WithRequestSize(base, 0)); ok {
		t.Errorf("WithRequestSize(err, 0) must not annotate the error")
	}
	if _, ok := RequestSizeFromError(base); ok {
		t.Error("RequestSizeFromError on unannotated error must return false")
	}
	if _, ok := RequestSizeFromError(nil); ok {
		t.Error("RequestSizeFromError(nil) must return false")
	}
}
