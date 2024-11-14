package cli

import (
	"fmt"
	"sync"
	"time"
)

type AnimationManager struct {
	wg   sync.WaitGroup
	done chan bool
}

func NewAnimationManager() *AnimationManager {
	return &AnimationManager{}
}

func (am *AnimationManager) ShowThinkingAnimation(clientName string) {
	am.wg.Add(1)
	am.done = make(chan bool)

	go func() {
		defer am.wg.Done()
		spinner := []string{"|", "/", "-", "\\"}
		i := 0
		for {
			select {
			case <-am.done:
				fmt.Printf("\r\033[K") // Limpa a linha corretamente
				return
			default:
				fmt.Printf("\r%s está pensando... %s", clientName, spinner[i%len(spinner)])
				time.Sleep(100 * time.Millisecond)
				i++
			}
		}
	}()
}

func (am *AnimationManager) StopThinkingAnimation() {
	close(am.done)
	am.wg.Wait()
	fmt.Printf("\n") // Garante que a próxima saída comece em uma nova linha
}
