package cli

import (
	"sync"
	"testing"
	"time"
)

func TestAnimationManager(t *testing.T) {
	am := NewAnimationManager()
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		am.ShowThinkingAnimation("TestClient")
	}()

	time.Sleep(500 * time.Millisecond)
	am.StopThinkingAnimation()
	wg.Wait()
}
