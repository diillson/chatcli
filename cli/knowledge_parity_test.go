/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/registry"
	"go.uber.org/zap"
)

// offeredAt is the set of completions offered at the end of line.
func offeredAt(c *ChatCLI, line string) map[string]bool {
	out := map[string]bool{}
	for _, s := range c.completer(docWithCursor(line, len(line))) {
		out[s.Text] = true
	}
	return out
}

// mustOffer checks that each line offers at least the listed completions.
func mustOffer(t *testing.T, c *ChatCLI, cases map[string][]string) {
	t.Helper()
	for line, want := range cases {
		for _, w := range want {
			if got := offeredAt(c, line); !got[w] {
				t.Errorf("%q must offer %q, got %v", line, w, got)
			}
		}
	}
}

func TestMemoryCommand_ForgetFormsAndFactIDs(t *testing.T) {
	c := newTestCLIWithMemory(t)
	mgr := c.memoryStore.Manager()
	mgr.RememberFact("User prefers tabs over spaces", "preference")
	mgr.RememberFact("The deploy runs on Fridays", "project")
	run := func(in string) string {
		return captureStdout(t, func() { c.handleMemoryCommand(context.Background(), in) })
	}
	tabsID := shortFactID(mgr.FactsMatching("tabs")[0].ID)

	if out := run("/memory forget tabs --dry-run"); !strings.Contains(out, "tabs over spaces") || !strings.Contains(out, tabsID) {
		t.Fatalf("dry run must list what it would remove, with ids: %q", out)
	}
	// None of these may delete: a preview, a mistyped flag, a flag missing
	// its value, an id mixed with a text, and no argument at all.
	for _, in := range []string{"/memory forget tabs --dryrun", "/memory forget --id", "/memory forget --id " + tabsID + " tabs", "/memory forget", "/memory forget --id zzzzzz"} {
		if out := run(in); len(mgr.Facts.GetAll()) != 2 || strings.TrimSpace(out) == "" {
			t.Fatalf("%q must report and remove nothing (facts=%d, out=%q)", in, len(mgr.Facts.GetAll()), out)
		}
	}

	if out := run("/memory facts"); !strings.Contains(out, tabsID) || !strings.Contains(out, "Fridays") {
		t.Fatalf("facts must show every fact with its id: %q", out)
	}
	if out := run("/memory facts --match fridays"); !strings.Contains(out, "Fridays") || strings.Contains(out, "tabs") {
		t.Fatalf("--match must filter by content: %q", out)
	}
	if out := run("/memory facts preference"); !strings.Contains(out, "tabs") || strings.Contains(out, "Fridays") {
		t.Fatalf("the category filter must still work: %q", out)
	}
	if out := run("/memory facts --limit 1"); !strings.Contains(out, i18n.T("kp.memory.facts.shown", 1, 2)) {
		t.Fatalf("--limit must cap the listing and say so: %q", out)
	}
	for _, in := range []string{"/memory facts --limit abc", "/memory facts --limit", "/memory facts --sort score"} {
		if out := run(in); strings.Contains(out, "Fridays") || strings.TrimSpace(out) == "" {
			t.Fatalf("%q must be reported instead of listing: %q", in, out)
		}
	}

	run("/memory forget --id " + tabsID)
	if left := mgr.Facts.GetAll(); len(left) != 1 || !strings.Contains(left[0].Content, "Fridays") {
		t.Fatalf("--id must remove exactly that fact, left %v", left)
	}
	run("/memory forget fridays")
	if n := len(mgr.Facts.GetAll()); n != 0 {
		t.Fatalf("forget <text> must still remove matching facts, %d left", n)
	}
}

func TestMemoryCompletion_FactFlags(t *testing.T) {
	c := newTestCLIWithMemory(t)
	mustOffer(t, c, map[string][]string{
		"/memory forget ":          {"--id", "--dry-run"},
		"/memory facts ":           {"preference", "--match", "--limit"},
		"/memory facts gotcha --l": {"--limit"},
	})
	for _, line := range []string{"/memory facts --match ", "/memory forget --id "} {
		if got := offeredAt(c, line); len(got) != 0 {
			t.Errorf("%q: a flag value has nothing to complete, got %v", line, got)
		}
	}
}

func TestMemoryAdapter_StatsAndWhy(t *testing.T) {
	c := newTestCLIWithMemory(t)
	c.memoryStore.Manager().RememberFact("User prefers tabs over spaces", "preference")
	a := &memoryPluginAdapter{cli: c}

	if out, err := a.Stats(); err != nil || !strings.Contains(out, i18n.T("mem.cmd.stats.facts_stored")+" 1") {
		t.Fatalf("stats = %q, %v", out, err)
	}
	if out, err := a.WhyLastRecall(); err != nil || out != i18n.T("memory.why.none") {
		t.Fatalf("why before any recall = %q, %v", out, err)
	}
	fact := c.memoryStore.Manager().Facts.GetAll()[0]
	c.rememberRecallTrace("tabs", []memory.RankedFact{{Fact: fact, Lexical: 0.5, Final: 0.9}})
	out, err := a.WhyLastRecall()
	if err != nil || !strings.Contains(out, "tabs over spaces") || !strings.Contains(out, "keywords") || !strings.Contains(out, shortFactID(fact.ID)) {
		t.Fatalf("why must list the recalled fact, its id and the signals: %q, %v", out, err)
	}
}

// evolvedSkillFixture points the authored-skills directory at a temp dir
// holding one skill whose backup says "original" and whose body says
// "worse" — the state a bad self-evolution leaves behind.
func evolvedSkillFixture(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	plugins.SetSkillsDirOverride(dir)
	t.Cleanup(func() { plugins.SetSkillsDirOverride("") })
	file := filepath.Join(dir, name, "SKILL.md")
	var backupErr error
	for _, step := range []func() error{
		func() error { return os.MkdirAll(filepath.Dir(file), 0o700) },
		func() error { return os.WriteFile(file, []byte("original"), 0o600) },
		func() error { _, backupErr = plugins.BackupSkill(name); return backupErr },
		func() error { return os.WriteFile(file, []byte("worse"), 0o600) },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	return file
}

func TestSkillCommand_ShowRestoreAndArgumentErrors(t *testing.T) {
	sh, _ := newPinTestFixture(t, map[string]string{"alpha": makeSkill("alpha", false)})
	run := func(in string) string {
		return captureStdout(t, func() { sh.HandleCommand(context.Background(), in) })
	}
	for in, want := range map[string]string{
		"/skill show alpha":               "body",
		"/skill show":                     i18n.T("kp.skill.usage.show"),
		"/skill show ghost":               i18n.T("kp.skill.show.not_found", "ghost"),
		"/skill restore":                  i18n.T("kp.skill.usage.restore"),
		"/skill install alpha --from":     i18n.T("kp.skill.from_missing"),
		"/skill info alpha --form x":      i18n.T("kp.flag.unknown", "--form"),
		"/skill show alpha --yes":         i18n.T("kp.skill.unexpected_arg", "--yes"),
		"/skill registry enable":          i18n.T("kp.skill.registry.usage"),
		"/skill registry frobnicate fake": i18n.T("kp.skill.registry.usage"),
	} {
		if out := run(in); !strings.Contains(out, want) {
			t.Errorf("%q must print %q, got %q", in, want, out)
		}
	}
	if out := run("/skill registry enable"); strings.Contains(out, sh.registryMgr.GetConfigPath()) {
		t.Errorf("an action missing its name must not fall back to the listing: %q", out)
	}
}

// Restore overwrites a file and consumes its backup, and /skill runs
// headless over ACP: without --yes it must only describe the change.
func TestSkillRestore_PreviewsWithoutYes(t *testing.T) {
	c := completerSkillFixture(t, map[string]string{"alpha": "---\nname: alpha\ndescription: a\n---\nproject body\n"})
	sh := c.skillHandler
	run := func(in string) string {
		return captureStdout(t, func() { sh.HandleCommand(context.Background(), in) })
	}
	file := evolvedSkillFixture(t, "alpha")
	for _, in := range []string{"/skill restore alpha", "/skill restore alpha --force", "/skill restore alpha beta --yes"} {
		out := run(in)
		if data, _ := os.ReadFile(file); string(data) != "worse" || !plugins.HasBackup("alpha") {
			t.Fatalf("%q must change nothing (body %q, backup %v): %q", in, data, plugins.HasBackup("alpha"), out)
		}
	}
	out := run("/skill restore alpha")
	if !strings.Contains(out, file) || !strings.Contains(out, file+".bak") || !strings.Contains(out, "--yes") {
		t.Fatalf("the preview must name the file, its backup and --yes: %q", out)
	}
	// The session loads "alpha" from the project, restore acts on the
	// authored copy: both commands must say they are different files.
	loaded, _ := sh.personaMgr.GetSkillByName("alpha")
	note := i18n.T("kp.skill.other_file", "alpha", loaded.Path, file)
	if show := run("/skill show alpha"); !strings.Contains(out, note) || !strings.Contains(show, note) || !strings.Contains(show, "project body") {
		t.Fatalf("show and restore must say they refer to different files:\nrestore: %q\nshow: %q", out, show)
	}
	if out := run("/skill restore alpha --yes"); readFileString(t, file) != "original" || !strings.Contains(out, i18n.T("skill.tool.restored", "alpha")) {
		t.Fatalf("--yes must perform the restore: %q", out)
	}
	if out := run("/skill restore alpha --yes"); !strings.Contains(out, i18n.T("skill.tool.restore_none", "alpha")) {
		t.Fatalf("a second restore has nothing left: %q", out)
	}
}

func TestMemoryFlags_RepeatedTerminatorAndSpacing(t *testing.T) {
	c := newTestCLIWithMemory(t)
	mgr := c.memoryStore.Manager()
	mgr.RememberFact("Always commit with --no-verify disabled", "gotcha")
	mgr.Facts.AddFact("The table has  two spaces in its header", "project", nil) // imported/extracted facts keep their spacing
	run := func(in string) string {
		return captureStdout(t, func() { c.handleMemoryCommand(context.Background(), in) })
	}
	ids := []string{shortFactID(mgr.Facts.GetAll()[0].ID), shortFactID(mgr.Facts.GetAll()[1].ID)}

	if out := run("/memory forget --id " + ids[0] + " --id " + ids[1]); len(mgr.Facts.GetAll()) != 2 || !strings.Contains(out, i18n.T("kp.flag.repeated", "--id")) {
		t.Fatalf("a repeated --id must be refused and remove nothing: %q", out)
	}
	if out := run("/memory facts --match table --match commit"); strings.Contains(out, "header") || !strings.Contains(out, i18n.T("kp.flag.repeated", "--match")) {
		t.Fatalf("a repeated --match must be refused: %q", out)
	}
	if out := run("/memory forget --no-verify"); len(mgr.Facts.GetAll()) != 2 || !strings.Contains(out, "--no-verify") || !strings.Contains(out, " -- ") {
		t.Fatalf("an unknown flag must point at the -- terminator: %q", out)
	}
	if out := run("/memory forget has  two spaces --dry-run"); !strings.Contains(out, "header") {
		t.Fatalf("the text must match with the spacing it was typed with: %q", out)
	}
	if out := run("/memory forget --dry-run -- --no-verify"); !strings.Contains(out, "Always commit") || len(mgr.Facts.GetAll()) != 2 {
		t.Fatalf("flags before -- still apply to the text after it: %q", out)
	}
	run("/memory forget -- --no-verify")
	if left := mgr.Facts.GetAll(); len(left) != 1 || !strings.Contains(left[0].Content, "header") {
		t.Fatalf("text after -- must be forgotten as text, left %v", left)
	}
}

func TestSkillCompletion_PreferShowRestore(t *testing.T) {
	c := completerSkillFixture(t, map[string]string{
		"local--alpha": "---\nname: alpha\ndescription: a\n---\nbody\n",
		"local--bravo": "---\nname: bravo\ndescription: b\n---\nbody\n",
	})
	evolvedSkillFixture(t, "alpha")
	mustOffer(t, c, map[string][]string{
		"/skill ":               {"show", "restore"},
		"/skill prefer ":        {"alpha", "bravo"},
		"/skill prefer alpha ":  {"local", "--reset"},
		"/skill show ":          {"alpha", "bravo"},
		"/skill restore ":       {"alpha"},
		"/skill restore alpha ": {"--yes"},
	})
	if got := offeredAt(c, "/skill restore "); got["bravo"] {
		t.Errorf("restore must offer only skills that have a backup, got %v", got)
	}
	if got := offeredAt(c, "/skill prefer alpha local "); len(got) != 0 {
		t.Errorf("nothing follows the source, got %v", got)
	}
	if got := offeredAt(c, "/session transcript export "); !got["md"] || !got["jsonl"] {
		t.Errorf("transcript export must offer its formats, got %v", got)
	}
}

func TestSkillAdapter_PinAndRegistryLookups(t *testing.T) {
	sh, _ := newPinTestFixture(t, map[string]string{"alpha": makeSkill("alpha", false), "manual": makeSkill("manual", true)})
	const entry = `{"name":"frontend-design","slug":"acme/frontend-design","description":"UI skill","version":"1.2.0","author":"acme","downloads":1500}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/skills/search":
			_, _ = w.Write([]byte(`{"skills":[` + entry + `]}`))
		case "/skills/frontend-design":
			_, _ = w.Write([]byte(entry))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	installDir := t.TempDir()
	mgr, err := registry.NewRegistryManager(registry.RegistriesConfig{
		InstallDir: installDir,
		Registries: []registry.RegistryEntry{{Name: "fake", URL: srv.URL, IsActive: true, Type: "custom"}},
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	sh.registryMgr = mgr
	a := &skillPluginAdapter{cli: &ChatCLI{skillHandler: sh}}
	ctx := context.Background()

	if out, err := a.Pin("alpha"); err != nil || !sh.IsPinned("alpha") || out != i18n.T("kp.skill.tool.pinned", "alpha") {
		t.Fatalf("pin = %q, %v", out, err)
	}
	if out, _ := a.Pin("alpha"); out != i18n.T("kp.skill.tool.pin_already", "alpha") {
		t.Fatalf("second pin = %q", out)
	}
	if _, err := a.Pin("manual"); err == nil || sh.IsPinned("manual") {
		t.Fatalf("a manual-only skill must not be pinnable by the tool: %v", err)
	}
	if _, err := a.Pin("ghost"); err == nil {
		t.Fatal("pinning an unknown skill must fail")
	}
	if out, err := a.Unpin("alpha"); err != nil || sh.IsPinned("alpha") || out != i18n.T("kp.skill.tool.unpinned", "alpha") {
		t.Fatalf("unpin = %q, %v", out, err)
	}
	if out, _ := a.Unpin("alpha"); out != i18n.T("kp.skill.tool.not_pinned", "alpha") {
		t.Fatalf("second unpin = %q", out)
	}

	out, err := a.RegistrySearch(ctx, "frontend")
	if err != nil || !strings.Contains(out, "- frontend-design v1.2.0 [fake]") || !strings.Contains(out, "UI skill") || !strings.Contains(out, i18n.T("kp.skill.tool.install_is_users")) {
		t.Fatalf("search = %q, %v", out, err)
	}
	for _, from := range []string{"", "fake"} {
		if out, err := a.RegistryInfo(ctx, "frontend-design", from); err != nil || !strings.Contains(out, "v1.2.0") || !strings.Contains(out, "UI skill") {
			t.Fatalf("info from %q = %q, %v", from, out, err)
		}
	}
	writeInstalledSkill(t, installDir, "local--homegrown", "---\nname: homegrown\ndescription: made here\n---\nbody\n")
	if out, err := a.RegistryInfo(ctx, "homegrown", ""); err != nil || !strings.Contains(out, "made here") || strings.Contains(out, i18n.T("kp.skill.tool.install_is_users")) {
		t.Fatalf("info of a local-only skill = %q, %v", out, err)
	}
	if _, err := a.RegistryInfo(ctx, "ghost", ""); err == nil {
		t.Fatal("info of an unknown skill must fail")
	}
}

// attachFixture is a CLI on a fake hub with one saved session, "prior",
// holding two messages.
func attachFixture(t *testing.T) (*ChatCLI, *fakeHubClient, *sessionPluginAdapter) {
	t.Helper()
	sm := newTestSessionManager(t)
	if err := sm.SaveSessionV2("prior", &SessionData{Version: 2, ChatHistory: []models.Message{{Role: "user", Content: "old"}, {Role: "assistant", Content: "answer"}}}); err != nil {
		t.Fatal(err)
	}
	hub := newFakeHubClient()
	c := &ChatCLI{sessionManager: sm, logger: zap.NewNop(), costTracker: NewCostTrackerAt(t.TempDir())}
	c.hubSync = newHubSync(hub, c.logger)
	return c, hub, &sessionPluginAdapter{cli: c}
}

func TestSessionAdapter_AttachSideEffectsAndStatus(t *testing.T) {
	c, hub, a := attachFixture(t)
	sm, ctx := c.sessionManager, context.Background()

	if out, err := a.Status(ctx); err != nil || out != i18n.T("kp.session.tool.status_unbound") {
		t.Fatalf("status unbound = %q, %v", out, err)
	}
	if _, err := a.Attach(ctx, "prior"); err != nil {
		t.Fatal(err)
	}
	if hub.convCounter != 2 {
		t.Fatalf("loading a session into an empty conversation must rotate the hub thread, conversations=%d", hub.convCounter)
	}
	if !c.costTracker.cache.rebuildPending {
		t.Fatal("a loaded history must mark the next cache write as an expected rebuild")
	}
	if out, _ := a.Status(ctx); out != i18n.T("kp.session.tool.status_bound", "prior", 2) {
		t.Fatalf("status bound = %q", out)
	}

	// Re-binding to a session that does not exist yet: the watermark of
	// "prior" must not survive, or the first write-through reads the missing
	// file as deleted elsewhere and drops the binding.
	c.costTracker.cache.rebuildPending = false
	if _, err := a.Attach(ctx, "ghost"); err != nil {
		t.Fatal(err)
	}
	if !c.boundSessionSync.IsZero() || hub.convCounter != 2 || c.costTracker.cache.rebuildPending {
		t.Fatalf("lazy attach: sync=%v conversations=%d rebuild=%v", c.boundSessionSync, hub.convCounter, c.costTracker.cache.rebuildPending)
	}
	c.persistBoundSession()
	if c.currentSessionName != "ghost" || !sm.SessionExists("ghost") {
		t.Fatalf("the first write-through must create the session and keep the binding (bound to %q)", c.currentSessionName)
	}

	rpcCaptureActive.Store(true)
	defer rpcCaptureActive.Store(false)
	if out, _ := a.Status(ctx); out != i18n.T("kp.session.tool.status_surface") {
		t.Fatalf("status on a captured surface = %q", out)
	}
}

// The hub thread holds the backlog the gateway channels read; rotating
// deletes it. Only a conversation that became a different one may rotate.
func TestSessionAttach_NoopAndMergeKeepHubThread(t *testing.T) {
	c, hub, a := attachFixture(t)
	c.history = []models.Message{{Role: "user", Content: "in flight"}}
	ctx := context.Background()

	// Merge: the running conversation keeps its identity.
	if _, err := a.Attach(ctx, "prior"); err != nil || len(c.history) != 3 || hub.convCounter != 1 || !c.costTracker.cache.rebuildPending {
		t.Fatalf("merge: err=%v history=%d conversations=%d", err, len(c.history), hub.convCounter)
	}
	// Attaching again to the bound session, from the tool and the command.
	out, err := a.Attach(ctx, "prior")
	if err != nil || out != i18n.T("kp.session.attach.same", "prior") || len(c.history) != 3 || hub.convCounter != 1 {
		t.Fatalf("tool re-attach must change nothing: %q err=%v history=%d conversations=%d", out, err, len(c.history), hub.convCounter)
	}
	ch := &CommandHandler{cli: c}
	printed := captureStdout(t, func() { ch.handleSessionCommand(ctx, "/session attach prior") })
	if !strings.Contains(printed, i18n.T("kp.session.attach.same", "prior")) || len(c.history) != 3 || hub.convCounter != 1 {
		t.Fatalf("command re-attach must change nothing: %q history=%d conversations=%d", printed, len(c.history), hub.convCounter)
	}
}

func TestSessionTranscriptShow_ReportsBadNumbers(t *testing.T) {
	c := &ChatCLI{history: []models.Message{{Role: "user", Content: "one"}, {Role: "assistant", Content: "two"}, {Role: "user", Content: "three"}}}
	show := func(args ...string) string {
		return captureStdout(t, func() { c.handleSessionTranscript(append([]string{"show"}, args...), "") })
	}
	for _, args := range [][]string{{"abc"}, {"2", "x"}, {"0"}, {"2", "-1"}} {
		bad := args[len(args)-1]
		if out := show(args...); !strings.Contains(out, i18n.T("kp.session.transcript.bad_number", bad)) || strings.Contains(out, "one") || strings.Contains(out, "two") {
			t.Errorf("show %v must report %q and print no message, got %q", args, bad, out)
		}
	}
	if out := show("2", "1"); !strings.Contains(out, "two") || strings.Contains(out, "three") {
		t.Errorf("show 2 1 = %q", out)
	}
}
