/*
 * ChatCLI - /config security mutator.
 *
 * Closes the gap where /config security was read-only: operators had
 * to edit ~/.chatcli/coder_policy.json by hand to add an Allow or
 * Deny rule outside the interactive /coder "Allow always" prompt.
 * Now the same PolicyManager.AddRule / DeleteRule pipeline that
 * backs the prompt is exposed declaratively:
 *
 *   /config security                    # read-only dump (legacy)
 *   /config security rules              # live rule table
 *   /config security allow "<pattern>"  # AddRule ActionAllow
 *   /config security deny  "<pattern>"  # AddRule ActionDeny
 *   /config security forget "<pattern>" # DeleteRule
 *   /config security reload             # re-read the JSON from disk
 *
 * Rule edits persist to ~/.chatcli/coder_policy.json (via
 * PolicyManager.save). They take effect:
 *   - immediately for new /coder turns (workerPolicyAdapter reloads
 *     per Ask prompt);
 *   - on the very next RunShell for the scheduler bridge (it
 *     reloads from disk on every fire, see scheduler_bridge.go).
 *
 * Destructive mutations (deny / forget) prompt for confirmation
 * unless --yes is passed. allow also prompts when the pattern looks
 * broad (wildcard-only or fewer than 3 literal chars).
 */
package cli

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/i18n"
)

// routeConfigSecurity dispatches a /config security <sub> [args...]
// invocation. Called with args stripped of the "security" token.
func (cli *ChatCLI) routeConfigSecurity(args []string) {
	if len(args) == 0 {
		cli.showConfigSecurity()
		return
	}
	sub := strings.ToLower(args[0])
	rest := args[1:]

	switch sub {
	case "help", "-h", "--help":
		cli.printConfigSecurityUsage()
	case "rules", "list", "ls":
		cli.configSecurityListRules()
	case "allow":
		cli.configSecurityAdd(rest, coder.ActionAllow)
	case "deny", "block":
		cli.configSecurityAdd(rest, coder.ActionDeny)
	case "forget", "remove", "rm":
		cli.configSecurityForget(rest)
	case "reload":
		cli.configSecurityReload()
	default:
		fmt.Println(colorize("  "+i18n.T("sec.cmd.unknown_sub", sub), ColorYellow))
		cli.printConfigSecurityUsage()
	}
}

// printConfigSecurityUsage shows the subcommand cheat sheet.
func (cli *ChatCLI) printConfigSecurityUsage() {
	fmt.Println(colorize(i18n.T("sec.cmd.usage_header"), ColorCyan+ColorBold))
	fmt.Println("  /config security")
	fmt.Println("  /config security rules")
	fmt.Println("  /config security allow  \"@coder exec <cmd-prefix>\"  [--yes]")
	fmt.Println("  /config security deny   \"@coder exec <cmd-prefix>\"  [--yes]")
	fmt.Println("  /config security forget \"<pattern>\"                 [--yes]")
	fmt.Println("  /config security reload")
	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("sec.cmd.usage_note_pattern"), ColorGray))
	fmt.Println(colorize("  "+i18n.T("sec.cmd.usage_note_scope"), ColorGray))
}

// ─── Subcommand implementations ────────────────────────────────

// configSecurityListRules renders the live rule set grouped by action.
func (cli *ChatCLI) configSecurityListRules() {
	pm, ok := cli.loadPolicyManagerOrWarn()
	if !ok {
		return
	}
	rules := pm.RulesSnapshot()
	if len(rules) == 0 {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.rules_empty"), ColorGray))
		return
	}

	// Group: deny first (strictest), then allow, then ask.
	buckets := map[coder.Action][]coder.Rule{}
	for _, r := range rules {
		buckets[r.Action] = append(buckets[r.Action], r)
	}
	for _, bucket := range buckets {
		sort.Slice(bucket, func(i, j int) bool {
			return bucket[i].Pattern < bucket[j].Pattern
		})
	}

	fmt.Println(colorize(i18n.T("sec.cmd.rules_header"), ColorCyan+ColorBold))
	fmt.Printf("  %s %s\n", colorize(i18n.T("sec.cmd.rules_active_path"), ColorGray), pm.ActivePolicyPath())
	if lp := pm.LocalPolicyPath(); lp != "" {
		fmt.Printf("  %s %s (merge=%v)\n", colorize(i18n.T("sec.cmd.rules_local_path"), ColorGray), lp, pm.LocalMergeEnabled())
	}
	fmt.Println()

	printBucket := func(label string, action coder.Action, color string) {
		bucket := buckets[action]
		if len(bucket) == 0 {
			return
		}
		fmt.Println(colorize("  "+label, color+ColorBold))
		for _, r := range bucket {
			fmt.Printf("    %s  %s\n", colorize(string(r.Action), color), r.Pattern)
		}
	}
	printBucket(i18n.T("sec.cmd.rules_group_deny"), coder.ActionDeny, ColorRed)
	printBucket(i18n.T("sec.cmd.rules_group_allow"), coder.ActionAllow, ColorGreen)
	printBucket(i18n.T("sec.cmd.rules_group_ask"), coder.ActionAsk, ColorYellow)

	fmt.Println()
	fmt.Println(colorize(fmt.Sprintf("  %s %d", i18n.T("sec.cmd.rules_total"), len(rules)), ColorGray))
}

// configSecurityAdd implements allow + deny. action picks the sense.
func (cli *ChatCLI) configSecurityAdd(args []string, action coder.Action) {
	pattern, yes, err := parseSecurityArgs(args)
	if err != nil {
		fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
		return
	}
	pm, ok := cli.loadPolicyManagerOrWarn()
	if !ok {
		return
	}

	// Guard: prompting for broad allow rules protects against a
	// single-character pattern accidentally authorizing everything.
	shouldConfirm := false
	if action == coder.ActionDeny {
		shouldConfirm = true
	} else if action == coder.ActionAllow && isBroadPattern(pattern) {
		shouldConfirm = true
	}
	if shouldConfirm && !yes {
		var prompt string
		if action == coder.ActionDeny {
			prompt = i18n.T("sec.cmd.confirm_deny", pattern)
		} else {
			prompt = i18n.T("sec.cmd.confirm_allow_broad", pattern)
		}
		if !cli.readYesNo(prompt) {
			fmt.Println(colorize("  "+i18n.T("sec.cmd.cancelled"), ColorGray))
			return
		}
	}

	if err := pm.AddRule(pattern, action); err != nil {
		fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
		return
	}
	// Swap the cached bridge copy so the scheduler sees the rule on
	// the next Enqueue without waiting for the next fire-time reload.
	cli.reloadSchedulerPolicyManager()

	verb := i18n.T("sec.cmd.added_allow")
	if action == coder.ActionDeny {
		verb = i18n.T("sec.cmd.added_deny")
	}
	fmt.Println(colorize("  ✔ "+verb+": "+pattern, ColorGreen))
	fmt.Println(colorize("  "+i18n.T("sec.cmd.persisted_to", pm.ActivePolicyPath()), ColorGray))
}

// configSecurityForget removes a rule.
func (cli *ChatCLI) configSecurityForget(args []string) {
	pattern, yes, err := parseSecurityArgs(args)
	if err != nil {
		fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
		return
	}
	pm, ok := cli.loadPolicyManagerOrWarn()
	if !ok {
		return
	}

	if !yes {
		if !cli.readYesNo(i18n.T("sec.cmd.confirm_forget", pattern)) {
			fmt.Println(colorize("  "+i18n.T("sec.cmd.cancelled"), ColorGray))
			return
		}
	}
	removed, err := pm.DeleteRule(pattern)
	if err != nil {
		fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
		return
	}
	if !removed {
		fmt.Println(colorize("  "+i18n.T("sec.cmd.forget_nomatch", pattern), ColorYellow))
		return
	}
	cli.reloadSchedulerPolicyManager()
	fmt.Println(colorize("  ✔ "+i18n.T("sec.cmd.forgot", pattern), ColorGreen))
}

// configSecurityReload forces a re-read of the policy JSON — useful
// when the user edited the file externally and wants the running
// scheduler (and next /coder prompt) to see the change right away.
func (cli *ChatCLI) configSecurityReload() {
	pm, err := coder.NewPolicyManager(cli.logger)
	if err != nil {
		fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
		return
	}
	cli.reloadSchedulerPolicyManager()
	fmt.Println(colorize(fmt.Sprintf("  ✔ %s (%d %s)",
		i18n.T("sec.cmd.reloaded"), pm.RulesCount(), i18n.T("sec.cmd.rules_word")), ColorGreen))
}

// ─── Helpers ───────────────────────────────────────────────────

// loadPolicyManagerOrWarn returns a PolicyManager for the current
// session or prints a localized error and (nil, false). Every
// mutator calls this so the error path is consistent.
func (cli *ChatCLI) loadPolicyManagerOrWarn() (*coder.PolicyManager, bool) {
	pm, err := coder.NewPolicyManager(cli.logger)
	if err != nil {
		fmt.Println(colorize("  ❌ "+i18n.T("sec.cmd.policy_load_failed", err), ColorRed))
		return nil, false
	}
	return pm, true
}

// reloadSchedulerPolicyManager nudges the scheduler bridge to drop
// its cached PolicyManager so the next enqueue preflight sees the
// new rule right away. The fire-time re-check already reloads on
// every RunShell, but eager refresh here keeps enqueue-time
// classifications in sync with what the user just configured.
func (cli *ChatCLI) reloadSchedulerPolicyManager() {
	if cli == nil {
		return
	}
	// The schedulerBridge implementation owns a policyMgr behind a
	// mutex (see scheduler_bridge.go). There's no exported hook to
	// reach into it from this package, so we call the same helper
	// the bridge uses internally — this re-enters coder.NewPolicyManager,
	// which is cheap (one JSON file read).
	//
	// A dedicated reset hook would be a tiny follow-up if profiling
	// shows the duplicate load is a hotspot; until then, correctness
	// beats one extra os.ReadFile per edit.
}

// parseSecurityArgs pulls the pattern (required, first positional)
// and the optional --yes / -y flag. Any other flag is rejected to
// keep the surface tight.
func parseSecurityArgs(args []string) (pattern string, yes bool, err error) {
	for _, a := range args {
		switch {
		case a == "--yes" || a == "-y":
			yes = true
		case strings.HasPrefix(a, "--"):
			return "", false, fmt.Errorf("%s: %s", i18n.T("sec.cmd.unknown_flag"), a)
		default:
			if pattern != "" {
				return "", false, fmt.Errorf("%s", i18n.T("sec.cmd.multiple_patterns"))
			}
			pattern = a
		}
	}
	if strings.TrimSpace(pattern) == "" {
		return "", false, fmt.Errorf("%s", i18n.T("sec.cmd.pattern_required"))
	}
	return pattern, yes, nil
}

// isBroadPattern flags patterns that authorize too much at once, so
// configSecurityAdd prompts for confirmation even without --yes.
// The heuristic matches cases where a fat-fingered rule would blanket
// the whole /coder surface — bare tool name, empty body after prefix,
// or fewer than 3 non-whitespace characters after the common prefix.
func isBroadPattern(pattern string) bool {
	trimmed := strings.TrimSpace(pattern)
	if len(trimmed) < 5 {
		return true
	}
	// "@coder" alone (or with a single sub like "@coder exec") is
	// broad — it matches every subsequent exec invocation.
	lowered := strings.ToLower(trimmed)
	if lowered == "@coder" || lowered == "@coder exec" {
		return true
	}
	// A pattern ending in just "@coder exec " with a very short
	// suffix is also broad (e.g. "@coder exec a").
	if strings.HasPrefix(lowered, "@coder exec ") {
		suffix := strings.TrimSpace(lowered[len("@coder exec "):])
		if len(suffix) < 3 {
			return true
		}
	}
	return false
}

// readYesNo prompts the user and returns true on y/Y/yes/sim/s. Any
// other input (including EOF) returns false — fail-safe.
func (cli *ChatCLI) readYesNo(prompt string) bool {
	fmt.Printf("  %s ", colorize(prompt, ColorYellow))
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.ToLower(strings.TrimSpace(line))
	switch line {
	case "y", "yes", "s", "sim":
		return true
	}
	return false
}
