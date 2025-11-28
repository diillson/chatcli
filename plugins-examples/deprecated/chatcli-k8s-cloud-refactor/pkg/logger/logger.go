package logger

import (
	"fmt"
	"io"
	"os"
	"time"
)

var Output io.Writer = os.Stderr

func Logf(format string, v ...interface{}) {
	fmt.Fprintf(Output, format, v...)
	if w, ok := Output.(*os.File); ok {
		w.Sync()
	}
}

func Info(msg string)                      { Logf("ℹ️  %s\n", msg) }
func Infof(f string, v ...interface{})     { Logf("ℹ️  "+f+"\n", v...) }
func Success(msg string)                   { Logf("✅ %s\n", msg) }
func Successf(f string, v ...interface{})  { Logf("✅ "+f+"\n", v...) }
func Warning(msg string)                   { Logf("⚠️  %s\n", msg) }
func Warningf(f string, v ...interface{})  { Logf("⚠️  "+f+"\n", v...) }
func Error(msg string)                     { Logf("❌ %s\n", msg) }
func Errorf(f string, v ...interface{})    { Logf("❌ "+f+"\n", v...) }
func Progress(msg string)                  { Logf("⏳ %s\n", msg) }
func Progressf(f string, v ...interface{}) { Logf("⏳ "+f+"\n", v...) }
func Debug(msg string) {
	if os.Getenv("DEBUG") == "1" {
		Logf("🔍 DEBUG: %s\n", msg)
	}
}
func Debugf(f string, v ...interface{}) {
	if os.Getenv("DEBUG") == "1" {
		Logf("🔍 DEBUG: "+f+"\n", v...)
	}
}
func Separator() {
	Logf("\n%s\n\n", "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
}

type Timer struct {
	name  string
	start time.Time
}

func NewTimer(name string) *Timer {
	return &Timer{name: name, start: time.Now()}
}

func (t *Timer) Stop() {
	Logf("⏱️  %s levou %v\n", t.name, time.Since(t.start).Round(time.Millisecond))
}
