package cli

import (
	"fmt"
	"sync"
	"time"
)

type AnimationManager struct {
	mu             sync.Mutex     // Para acesso seguro aos campos
	wg             sync.WaitGroup // Para esperar a goroutine terminar
	done           chan struct{}  // Canal para sinalizar encerramento
	stopRequested  bool           // Flag para rastrear se já solicitamos parada
	currentMessage string         // Mensagem atual sendo exibida
	isRunning      bool           // Estado da animação
}

func NewAnimationManager() *AnimationManager {
	return &AnimationManager{
		done: make(chan struct{}),
	}
}

// ShowThinkingAnimation inicia ou atualiza a animação "pensando"
func (am *AnimationManager) ShowThinkingAnimation(message string) {
	am.mu.Lock()
	defer am.mu.Unlock()

	// Atualiza a mensagem
	am.currentMessage = message

	// Se a animação já está rodando, apenas atualize a mensagem e retorne
	if am.isRunning {
		return
	}

	// Inicializa nova animação
	am.isRunning = true
	am.stopRequested = false
	am.done = make(chan struct{}) // Cria um novo canal

	am.wg.Add(1)
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
				// Acesso seguro à mensagem atual
				am.mu.Lock()
				currentMsg := am.currentMessage
				am.mu.Unlock()

				fmt.Printf("\r%s... %s", currentMsg, spinner[i%len(spinner)])
				time.Sleep(100 * time.Millisecond)
				i++
			}
		}
	}()
}

// UpdateMessage atualiza a mensagem sem parar e reiniciar a animação
func (am *AnimationManager) UpdateMessage(message string) {
	am.mu.Lock()
	defer am.mu.Unlock()

	am.currentMessage = message
}

// StopThinkingAnimation para a animação de forma segura
func (am *AnimationManager) StopThinkingAnimation() {
	am.mu.Lock()

	// Se a animação não está rodando ou já solicitamos parada, apenas retorne
	if !am.isRunning || am.stopRequested {
		am.mu.Unlock()
		return
	}

	// Marca que solicitamos parada e sinaliza o encerramento
	am.stopRequested = true
	am.isRunning = false

	// Mantém referência local ao canal antes de desbloqueá-lo
	done := am.done
	am.mu.Unlock()

	// Fecha o canal fora do lock para evitar deadlock
	close(done)

	// Aguarda a goroutine terminar
	am.wg.Wait()

	// Adiciona uma nova linha para garantir espaçamento adequado
	fmt.Println()
}
