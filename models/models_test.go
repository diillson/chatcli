package models

import "testing"

func TestMessage_IsValid(t *testing.T) {
	msg := Message{Role: "user", Content: "Olá"}
	if !msg.IsValid() {
		t.Error("Mensagem válida foi considerada inválida")
	}

	msg.Role = "invalid"
	if msg.IsValid() {
		t.Error("Mensagem inválida foi considerada válida")
	}
}

func TestResponseData_IsValid(t *testing.T) {
	resp := ResponseData{Status: "completed"}
	if !resp.IsValid() {
		t.Error("Resposta válida foi considerada inválida")
	}

	resp.Status = "unknown"
	if resp.IsValid() {
		t.Error("Resposta inválida foi considerada válida")
	}
}
