package utils

import (
	"fmt"
	"os"
)

func Logf(format string, v ...interface{}) {
	fmt.Fprintf(os.Stderr, format, v...)
	os.Stderr.Sync()
}

func Fatalf(format string, v ...interface{}) {
	fmt.Fprintf(os.Stderr, "❌ Error: "+format+"\n", v...)
	os.Exit(1)
}
