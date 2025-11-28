package logger

import (
	"fmt"
	"io"
	"os"
	"time"
)

var (
	// Output padrão é stderr para não interferir com stdout (usado pelo ChatCLI)
	Output io.Writer = os.Stderr
)

// Logf escreve mensagens formatadas para stderr
func Logf(format string, v ...interface{}) {
	fmt.Fprintf(Output, format, v...)
	if w, ok := Output.(*os.File); ok {
		w.Sync()
	}
}

// Info mensagem informativa
func Info(msg string) {
	Logf("ℹ️  %s\n", msg)
}

// Infof mensagem informativa formatada
func Infof(format string, v ...interface{}) {
	Logf("ℹ️  "+format+"\n", v...)
}

// Success mensagem de sucesso
func Success(msg string) {
	Logf("✅ %s\n", msg)
}

// Successf mensagem de sucesso formatada
func Successf(format string, v ...interface{}) {
	Logf("✅ "+format+"\n", v...)
}

// Warning mensagem de aviso
func Warning(msg string) {
	Logf("⚠️  %s\n", msg)
}

// Warningf mensagem de aviso formatada
func Warningf(format string, v ...interface{}) {
	Logf("⚠️  "+format+"\n", v...)
}

// Error mensagem de erro
func Error(msg string) {
	Logf("❌ %s\n", msg)
}

// Errorf mensagem de erro formatada
func Errorf(format string, v ...interface{}) {
	Logf("❌ "+format+"\n", v...)
}

// Fatal erro fatal (termina o programa)
func Fatal(msg string) {
	Errorf("FATAL: %s", msg)
	os.Exit(1)
}

// Fatalf erro fatal formatado
func Fatalf(format string, v ...interface{}) {
	Errorf("FATAL: "+format, v...)
	os.Exit(1)
}

// Progress mostra progresso de uma operação
func Progress(msg string) {
	Logf("⏳ %s\n", msg)
}

// Progressf progresso formatado
func Progressf(format string, v ...interface{}) {
	Logf("⏳ "+format+"\n", v...)
}

// Step indica um passo de um processo
func Step(num int, total int, msg string) {
	Logf("[%d/%d] %s\n", num, total, msg)
}

// Stepf step formatado
func Stepf(num int, total int, format string, v ...interface{}) {
	Logf("[%d/%d] "+format+"\n", append([]interface{}{num, total}, v...)...)
}

// Debug mensagem de debug (só mostra se DEBUG=1)
func Debug(msg string) {
	if os.Getenv("DEBUG") == "1" {
		Logf("🔍 DEBUG: %s\n", msg)
	}
}

// Debugf debug formatado
func Debugf(format string, v ...interface{}) {
	if os.Getenv("DEBUG") == "1" {
		Logf("🔍 DEBUG: "+format+"\n", v...)
	}
}

// Separator linha separadora
func Separator() {
	Logf("\n%s\n\n", "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
}

// Timer para medir tempo de execução
type Timer struct {
	name  string
	start time.Time
}

// NewTimer cria um novo timer
func NewTimer(name string) *Timer {
	return &Timer{
		name:  name,
		start: time.Now(),
	}
}

// Stop para o timer e mostra o tempo decorrido
func (t *Timer) Stop() {
	elapsed := time.Since(t.start)
	Logf("⏱️  %s levou %v\n", t.name, elapsed.Round(time.Millisecond))
}
