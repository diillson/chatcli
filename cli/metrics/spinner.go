package metrics

import (
	"sync"
	"time"
)

var spinnerFrames = []string{"⋮", "⋯", "⋰", "⋱", "⋲", "⋳", "⋴", "⋵"}
var dotsFrames = []string{"   ", ".  ", ".. ", "..."}

type Spinner struct {
	frameIndex int
	mu         sync.Mutex
}

func NewSpinner() *Spinner {
	return &Spinner{}
}

func (s *Spinner) Next() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	frame := spinnerFrames[s.frameIndex]
	s.frameIndex = (s.frameIndex + 1) % len(spinnerFrames)
	return frame
}

var globalSpinner = NewSpinner()
var globalDotsIndex int
var lastUpdate time.Time
var spinnerMu sync.Mutex

func GetSpinnerFrame() string {
	spinnerMu.Lock()
	defer spinnerMu.Unlock()
	now := time.Now()
	if now.Sub(lastUpdate) >= 100*time.Millisecond {
		lastUpdate = now
		return globalSpinner.Next()
	}
	if globalSpinner.frameIndex == 0 {
		return spinnerFrames[len(spinnerFrames)-1]
	}
	return spinnerFrames[globalSpinner.frameIndex-1]
}

func GetDotsAnimation() string {
	spinnerMu.Lock()
	defer spinnerMu.Unlock()
	now := time.Now()
	if now.Sub(lastUpdate) >= 500*time.Millisecond {
		globalDotsIndex = (globalDotsIndex + 1) % len(dotsFrames)
	}
	return dotsFrames[globalDotsIndex]
}
