package models

import "github.com/diillson/chatcli/config"

// Message representa uma mensagem trocada com o modelo de linguagem.
type Message struct {
	Role    string `json:"role"`    // O papel da mensagem, como "user" ou "assistant".
	Content string `json:"content"` // O conteúdo da mensagem.
}

// IsValid valida se a mensagem tem um papel e conteúdo válidos.
func (m *Message) IsValid() bool {
	validRoles := map[string]bool{
		"user":      true,
		"assistant": true,
	}
	return validRoles[m.Role] && m.Content != ""
}

// ResponseData representa os dados de resposta da LLM.
type ResponseData struct {
	Status   string `json:"status"`   // O status da resposta: "processing", "completed", ou "error".
	Response string `json:"response"` // A resposta da LLM, se o status for "completed".
	Message  string `json:"message"`  // Mensagem de erro, se o status for "error".
}

// IsValid valida se o status da resposta é um dos valores esperados.
func (r *ResponseData) IsValid() bool {
	validStatuses := map[string]bool{
		config.StatusProcessing: true,
		config.StatusCompleted:  true,
		config.StatusError:      true,
	}
	return validStatuses[r.Status]
}

//// Exemplo de teste unitário para a serialização e desserialização de Message
//func TestMessageSerialization() error {
//	msg := Message{
//		Role:    "user",
//		Content: "Hello, world!",
//	}
//
//	// Serializar a mensagem para JSON
//	jsonData, err := json.Marshal(msg)
//	if err != nil {
//		return fmt.Errorf("erro ao serializar a mensagem: %v", err)
//	}
//
//	// Desserializar a mensagem de volta para a struct
//	var deserializedMsg Message
//	if err := json.Unmarshal(jsonData, &deserializedMsg); err != nil {
//		return fmt.Errorf("erro ao desserializar a mensagem: %v", err)
//	}
//
//	// Verificar se os dados desserializados correspondem aos dados originais
//	if deserializedMsg.Role != msg.Role || deserializedMsg.Content != msg.Content {
//		return fmt.Errorf("os dados desserializados não correspondem aos dados originais")
//	}
//
//	return nil
//}
//
//// Exemplo de teste unitário para a validação de ResponseData
//func TestResponseDataValidation() error {
//	response := ResponseData{
//		Status:   StatusCompleted,
//		Response: "This is the response",
//		Message:  "",
//	}
//
//	if !response.IsValid() {
//		return fmt.Errorf("a validação falhou para um status válido")
//	}
//
//	// Testar com um status inválido
//	response.Status = "invalid_status"
//	if response.IsValid() {
//		return fmt.Errorf("a validação não detectou um status inválido")
//	}
//
//	return nil
//}
