package client

import (
	"encoding/json"
	"testing"
)

// The refresh body keeps everything that is part of the cache key and
// drops only what a no-output request cannot carry.
func TestKeepAliveRequestBodyRewritesOnlyTheOutputSide(t *testing.T) {
	last := []byte(`{"model":"m","max_tokens":4096,"max_completion_tokens":4096,"stream":true,"stream_options":{"include_usage":true},
		"thinking":{"type":"adaptive"},"output_config":{"effort":"high","task_budget":{"type":"tokens","total":50000}},
		"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"t"}]}`)
	body, err := KeepAliveRequestBody(last, 1)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["max_tokens"] != float64(1) || got["max_completion_tokens"] != float64(1) {
		t.Errorf("output caps must be lowered under both names: %v", got)
	}
	if _, ok := got["stream"]; ok {
		t.Error("stream must be dropped")
	}
	if _, ok := got["stream_options"]; ok {
		t.Error("stream_options must be dropped")
	}
	cfg, _ := got["output_config"].(map[string]interface{})
	if cfg["effort"] != "high" || cfg["task_budget"] != nil {
		t.Errorf("effort stays, task budget goes: %v", cfg)
	}
	if got["thinking"] == nil || got["tools"] == nil || got["messages"] == nil {
		t.Error("thinking, tools and messages are the cache key and must stay")
	}
	body, _ = KeepAliveRequestBody([]byte(`{"max_tokens":10,"output_config":{"task_budget":{}}}`), -5)
	var small map[string]interface{}
	_ = json.Unmarshal(body, &small)
	if small["max_tokens"] != float64(0) || small["output_config"] != nil {
		t.Errorf("a negative minimum clamps to zero and an emptied output_config is removed: %v", small)
	}
	if _, err := KeepAliveRequestBody([]byte(`nope`), 0); err == nil {
		t.Error("a body that is not JSON is an error")
	}
}

func TestLastRequestKeeper(t *testing.T) {
	var k LastRequestKeeper
	if _, ok := k.Take(); ok {
		t.Fatal("nothing remembered yet")
	}
	k.Remember([]byte(`{"a":1}`))
	if b, ok := k.Take(); !ok || string(b) != `{"a":1}` {
		t.Fatalf("got %q %v", b, ok)
	}
	var none *LastRequestKeeper
	none.Remember([]byte("x"))
	if _, ok := none.Take(); ok {
		t.Fatal("nil keeper holds nothing")
	}
}
