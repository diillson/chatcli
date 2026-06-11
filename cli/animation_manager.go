package cli

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/diillson/chatcli/ui/theme"
	"golang.org/x/term"
)

// spinnerFrames is the single source of the braille spinner animation, shared
// by the "thinking" AnimationManager and the interactive prompt-prefix
// spinner so both surfaces animate identically. Braille dots read as a smooth
// rotation in every modern terminal font.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type AnimationManager struct {
	mu             sync.Mutex     // Para acesso seguro aos campos
	wg             sync.WaitGroup // Para esperar a goroutine terminar
	done           chan struct{}  // Canal para sinalizar encerramento
	stopRequested  bool           // Flag para rastrear se já solicitamos parada
	currentMessage string         // Mensagem atual sendo exibida
	isRunning      bool           // Estado da animação
	suppressed     bool           // When true, ShowThinkingAnimation won't start the goroutine
}

func NewAnimationManager() *AnimationManager {
	return &AnimationManager{
		done: make(chan struct{}),
	}
}

// SetSuppressed enables or disables animation suppression. When suppressed,
// ShowThinkingAnimation stores the message but does not start the spinner goroutine.
// This prevents the animation from conflicting with go-prompt's rendering.
func (am *AnimationManager) SetSuppressed(v bool) {
	am.mu.Lock()
	am.suppressed = v
	am.mu.Unlock()
}

// ShowThinkingAnimation inicia ou atualiza a animação "pensando"
func (am *AnimationManager) ShowThinkingAnimation(message string) {
	am.mu.Lock()
	defer am.mu.Unlock()

	// Atualiza a mensagem
	am.currentMessage = message

	// If suppressed, store message but don't start the goroutine. Also skip
	// when stdout is not a terminal: the carriage-return repaints would be
	// noise in a pipe / CI log (the message is still recorded for callers
	// that surface it some other way).
	if am.suppressed || !theme.ActiveProfile().IsTerminal() {
		return
	}

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
		i := 0
		for {
			select {
			case <-am.done:
				// Limpar a linha E resetar cores ANSI
				fmt.Print("\r\033[K\033[0m")
				_ = os.Stdout.Sync() // Força flush do buffer
				return
			default:
				// Acesso seguro à mensagem atual
				am.mu.Lock()
				currentMsg := am.currentMessage
				am.mu.Unlock()

				// Message tinted with the theme accent (matches reasoning
				// cards); the glyph carries the same accent so the spinner
				// reads as one themed unit. Reset closes the span so nothing
				// bleeds into following output.
				//
				// The message is clamped to ONE terminal line: the repaint
				// protocol (\r + \033[K) only rewinds to the start of the
				// current visual line, so a message wider than the terminal
				// wraps and leaves a stale copy behind on EVERY tick — ten
				// junk lines per second until the tool returns. Queried per
				// tick so live terminal resizes are honored.
				accent := theme.ANSI(theme.RoleReasoning)
				reset := theme.Reset()
				glyph := spinnerFrames[i%len(spinnerFrames)]
				display := clampSpinnerMessage(currentMsg, terminalCols())
				fmt.Printf("\r\033[K%s%s...%s %s%s%s", accent, display, reset, accent, glyph, reset)
				_ = os.Stdout.Sync() // Força flush

				time.Sleep(100 * time.Millisecond)
				i++
			}
		}
	}()
}

// terminalCols returns the current stdout width, with a conservative default
// when stdout is not a terminal or the size cannot be determined.
func terminalCols() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return 100
}

// clampSpinnerMessage bounds msg so the rendered spinner line (message +
// "... " + glyph) never exceeds one terminal row. Rune-aware so multibyte
// labels (paths, queries, emoji) truncate cleanly.
func clampSpinnerMessage(msg string, cols int) string {
	const reserved = 8 // "... " + glyph + right margin
	limit := cols - reserved
	if limit < 16 {
		limit = 16
	}
	r := []rune(msg)
	if len(r) <= limit {
		return msg
	}
	return string(r[:limit-1]) + "…"
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

	// Garantir limpeza completa do terminal após parar
	fmt.Print("\033[0m") // Reset de cores
	_ = os.Stdout.Sync()

	// Adiciona uma nova linha para garantir espaçamento adequado
	fmt.Println()
}
