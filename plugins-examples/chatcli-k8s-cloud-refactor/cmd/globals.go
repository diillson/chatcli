package cmd

// GlobalFlags flags globais compartilhadas entre comandos
var GlobalFlags struct {
	DryRun  bool
	Force   bool
	Verbose bool
	Output  string
}
