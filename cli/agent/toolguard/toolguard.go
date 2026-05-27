/*
 * Package toolguard detects when the agent gets stuck repeatedly FAILING the
 * same tool, and produces targeted guidance to break the cycle.
 *
 * It complements the batch-level stagnation tracker (cli/agent_earlyexit.go),
 * which halts when an identical batch of tool calls repeats. That tracker is
 * order/arg-sensitive, so it misses two common failure modes this guard
 * catches:
 *
 *   - the same tool failing turn after turn with slightly DIFFERENT args
 *     (argument drift), which never produces an identical batch fingerprint;
 *   - the same exact tool+args failing repeatedly within close succession.
 *
 * The guard is advisory by design: Observe() returns a guidance string the
 * caller injects into history so the model can self-correct. It never removes
 * or alters existing control flow — it only adds feedback. Hard-halt is
 * opt-in via Config.HaltAfterSameSig.
 */
package toolguard

import (
	"sort"
	"strings"
	"sync"
)

// Config tunes the thresholds. Zero value yields sane defaults via New.
type Config struct {
	// WarnAfterToolFailures is the number of consecutive failures of the
	// SAME tool (any args) before guidance is emitted. Default 3.
	WarnAfterToolFailures int
	// WarnAfterSameSig is the number of consecutive failures of the EXACT
	// same tool+args before stronger guidance is emitted. Default 2.
	WarnAfterSameSig int
	// HaltAfterSameSig, when > 0, makes Observe set Halt on the decision
	// once an identical tool+args has failed this many times. 0 disables
	// hard halt (advisory only). Default 0.
	HaltAfterSameSig int
}

func (c Config) withDefaults() Config {
	if c.WarnAfterToolFailures <= 0 {
		c.WarnAfterToolFailures = 3
	}
	if c.WarnAfterSameSig <= 0 {
		c.WarnAfterSameSig = 2
	}
	return c
}

// Decision is the result of observing a tool outcome.
type Decision struct {
	// Guidance is non-empty when the model should be nudged to change
	// approach. The caller injects it into history.
	Guidance string
	// Halt is true only when Config.HaltAfterSameSig is set and reached.
	Halt bool
}

// Guard tracks per-tool and per-signature consecutive failures for one
// agent run. It is safe for concurrent use (parallel sub-tool execution).
type Guard struct {
	cfg Config

	mu        sync.Mutex
	toolFails map[string]int    // tool name -> consecutive failures
	sigFails  map[string]int    // tool+args signature -> consecutive failures
	lastErr   map[string]string // tool name -> last error message
	warnedSig map[string]bool   // signature -> already warned (avoid spam)
}

// New returns a Guard with the given config (defaults applied).
func New(cfg Config) *Guard {
	return &Guard{
		cfg:       cfg.withDefaults(),
		toolFails: map[string]int{},
		sigFails:  map[string]int{},
		lastErr:   map[string]string{},
		warnedSig: map[string]bool{},
	}
}

// Signature builds a stable identity for a tool call: name plus
// whitespace-normalized args.
func Signature(tool, args string) string {
	return tool + "|" + strings.Join(strings.Fields(args), " ")
}

// Observe records the outcome of a single tool execution and returns a
// Decision. failed reports whether the call errored; errMsg is the error
// text (used to make guidance concrete). A successful call resets the
// failure streak for that tool.
func (g *Guard) Observe(tool, args, errMsg string, failed bool) Decision {
	sig := Signature(tool, args)

	g.mu.Lock()
	defer g.mu.Unlock()

	if !failed {
		// Success clears this tool's streak and any per-sig counters that
		// share its name, so unrelated later failures start fresh.
		delete(g.toolFails, tool)
		delete(g.lastErr, tool)
		for s := range g.sigFails {
			if strings.HasPrefix(s, tool+"|") {
				delete(g.sigFails, s)
				delete(g.warnedSig, s)
			}
		}
		return Decision{}
	}

	g.toolFails[tool]++
	g.sigFails[sig]++
	if errMsg != "" {
		g.lastErr[tool] = truncate(errMsg, 200)
	}

	toolN := g.toolFails[tool]
	sigN := g.sigFails[sig]

	// Hard halt on identical repeated failure, if configured.
	if g.cfg.HaltAfterSameSig > 0 && sigN >= g.cfg.HaltAfterSameSig {
		return Decision{
			Guidance: g.haltMessage(tool, sigN),
			Halt:     true,
		}
	}

	// Strong guidance for identical repeated failure.
	if sigN >= g.cfg.WarnAfterSameSig && !g.warnedSig[sig] {
		g.warnedSig[sig] = true
		return Decision{Guidance: g.sameSigMessage(tool, sigN)}
	}

	// Softer guidance for the same tool failing with DRIFTING args. Only
	// fires when failures span more than one signature (sigN < toolN);
	// identical-arg streaks are owned by the same-sig branch above, so we
	// don't double-warn.
	if toolN == g.cfg.WarnAfterToolFailures && sigN < toolN {
		return Decision{Guidance: g.toolDriftMessage(tool, toolN)}
	}

	return Decision{}
}

func (g *Guard) sameSigMessage(tool string, n int) string {
	var b strings.Builder
	b.WriteString("[tool-loop] ")
	b.WriteString(tool)
	b.WriteString(" has failed ")
	b.WriteString(plural(n, "time"))
	b.WriteString(" with the same arguments. Do NOT retry it identically.")
	if e := g.lastErr[tool]; e != "" {
		b.WriteString(" Last error: ")
		b.WriteString(e)
	}
	b.WriteString(" Change the arguments or try a different approach.")
	return b.String()
}

func (g *Guard) toolDriftMessage(tool string, n int) string {
	var b strings.Builder
	b.WriteString("[tool-loop] ")
	b.WriteString(tool)
	b.WriteString(" has failed ")
	b.WriteString(plural(n, "time"))
	b.WriteString(" in a row. Step back and reconsider the approach instead of retrying variations.")
	if e := g.lastErr[tool]; e != "" {
		b.WriteString(" Last error: ")
		b.WriteString(e)
	}
	return b.String()
}

func (g *Guard) haltMessage(tool string, n int) string {
	return "[tool-loop halted] " + tool + " failed " + plural(n, "time") +
		" with identical arguments; stopping to avoid an infinite loop. " +
		"Rephrase the task or provide more context."
}

// sortedSigs is a test/diagnostic helper.
func (g *Guard) sortedSigs() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.sigFails))
	for s := range g.sigFails {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func plural(n int, unit string) string {
	s := itoa(n) + " " + unit
	if n != 1 {
		s += "s"
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
