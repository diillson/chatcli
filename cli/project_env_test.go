package cli

import (
	"testing"

	"github.com/diillson/chatcli/llm/manager"
)

// A server that cached the manager must follow every rebuild: without the
// hook, a `/config reload` issued from an MCP/ACP client changes the
// session's manager and leaves the server serving the previous provider set.
func TestOnManagerRebuild_NotifiesRegisteredServers(t *testing.T) {
	ResetManagerHooksForTest()
	t.Cleanup(ResetManagerHooksForTest)

	var got manager.LLMManager
	calls := 0
	(&ChatCLI{}).OnManagerRebuild(func(m manager.LLMManager) {
		got = m
		calls++
	})

	rebuilt := &manager.LLMManagerImpl{}
	notifyManagerRebuilt(rebuilt)

	if calls != 1 || got != manager.LLMManager(rebuilt) {
		t.Fatalf("hook must receive the rebuilt manager: calls=%d got=%v", calls, got)
	}

	// A nil rebuild is a no-op — never hand a server a nil manager.
	notifyManagerRebuilt(nil)
	if calls != 1 {
		t.Fatalf("nil must not fire the hook, calls=%d", calls)
	}
}

func TestOnManagerRebuild_IgnoresNilHook(t *testing.T) {
	ResetManagerHooksForTest()
	t.Cleanup(ResetManagerHooksForTest)
	(&ChatCLI{}).OnManagerRebuild(nil)
	notifyManagerRebuilt(&manager.LLMManagerImpl{}) // must not panic
}
