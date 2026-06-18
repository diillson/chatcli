package gateway

import (
	"encoding/json"
	"testing"
)

func TestParseDiscordHelloInterval(t *testing.T) {
	hb, err := parseDiscordHelloInterval([]byte(`{"op":10,"d":{"heartbeat_interval":41250}}`))
	if err != nil || hb != 41250 {
		t.Fatalf("hello parse wrong: hb=%d err=%v", hb, err)
	}
	if _, err := parseDiscordHelloInterval([]byte(`{"op":0,"d":{}}`)); err == nil {
		t.Error("non-hello op should error")
	}
}

func TestDecodeDiscordFrame(t *testing.T) {
	op, s, tp, _ := decodeDiscordFrame([]byte(`{"op":0,"s":42,"t":"MESSAGE_CREATE","d":{}}`))
	if op != 0 || s == nil || *s != 42 || tp != "MESSAGE_CREATE" {
		t.Errorf("frame decode wrong: op=%d s=%v t=%q", op, s, tp)
	}
}

func TestParseDiscordMessage(t *testing.T) {
	d := json.RawMessage(`{"channel_id":"C9","content":"hello bot","author":{"id":"U1","username":"neo","bot":false}}`)
	msg, ok := parseDiscordMessage(d)
	if !ok || msg.ChatID != "C9" || msg.UserID != "U1" || msg.Text != "hello bot" || msg.Platform != "discord" {
		t.Fatalf("message parse wrong: %+v ok=%v", msg, ok)
	}

	// Bot messages skipped (loop prevention).
	bot := json.RawMessage(`{"channel_id":"C9","content":"x","author":{"id":"B","bot":true}}`)
	if _, ok := parseDiscordMessage(bot); ok {
		t.Error("bot messages must be skipped")
	}
	// Empty content skipped.
	empty := json.RawMessage(`{"channel_id":"C9","content":"","author":{"id":"U"}}`)
	if _, ok := parseDiscordMessage(empty); ok {
		t.Error("empty content must be skipped")
	}
}

func TestDiscordIdentifyIntents(t *testing.T) {
	id := discordIdentify("tok")
	d := id["d"].(map[string]interface{})
	if d["token"] != "tok" {
		t.Error("identify must carry token")
	}
	if d["intents"].(int) != discordIntents {
		t.Errorf("intents wrong: %v", d["intents"])
	}
}

func TestParseWhatsAppInbound(t *testing.T) {
	body := []byte(`{"entry":[{"changes":[{"value":{"messages":[
		{"from":"5511999","type":"text","text":{"body":"oi"}},
		{"from":"5511888","type":"image","image":{"id":"IMG1","mime_type":"image/jpeg"}},
		{"from":"5511666","type":"image"},
		{"from":"5511777","type":"text","text":{"body":"  "}}
	]}}]}]}`)
	msgs := parseWhatsAppInbound(body)
	// A text message and an image-with-id are kept; an image without an id and
	// a blank-text message are skipped.
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (text + image), got %d", len(msgs))
	}
	if msgs[0].ChatID != "5511999" || msgs[0].Text != "oi" || msgs[0].Platform != "whatsapp" {
		t.Errorf("whatsapp parse wrong: %+v", msgs[0])
	}
	if msgs[1].Image == nil || msgs[1].Image.ref != "IMG1" || msgs[1].Image.MimeType != "image/jpeg" {
		t.Errorf("whatsapp image parse wrong: %+v", msgs[1].Image)
	}
}

func TestParseWhatsAppInbound_BadJSON(t *testing.T) {
	if msgs := parseWhatsAppInbound([]byte(`nope`)); msgs != nil {
		t.Error("bad json should yield nil")
	}
}
