package models

import (
	"github.com/diillson/chatcli/config"
	"sort"
)

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

// EstimateTokenCount estima o número de tokens em uma mensagem
func (m *Message) EstimateTokenCount() int {
	// Estimativa conservadora: cerca de 0.25 tokens por caractere
	return int(float64(len(m.Content)) * 0.25)
}

// TrimHistory reduz o tamanho do histórico para ficar abaixo do limite de tokens
// Mantém as mensagens mais recentes e informativas
func TrimHistory(history []Message, maxTokens int) []Message {
	// Se o histórico está vazio, não há nada a fazer
	if len(history) == 0 {
		return history
	}

	// Calcular o total de tokens no histórico atual
	totalTokens := 0
	for _, msg := range history {
		totalTokens += msg.EstimateTokenCount()
	}

	// Se já estamos abaixo do limite, retornar o histórico original
	if totalTokens <= maxTokens {
		return history
	}

	// Precisamos reduzir o histórico
	// Estratégia: manter a primeira mensagem (sistema) e as mensagens mais recentes

	// Classificar mensagens por importância
	type msgWithTokens struct {
		index      int
		msg        Message
		tokenCount int
		importance int // Maior = mais importante
	}

	msgs := make([]msgWithTokens, len(history))
	for i, msg := range history {
		importance := 0

		// Mensagens do sistema são importantes
		if msg.Role == "system" {
			importance += 100
		}

		// Mensagens recentes são importantes (últimas 4)
		if i >= len(history)-4 {
			importance += (len(history) - i) * 10
		}

		// A mensagem atual do usuário (última) é a mais importante
		if i == len(history)-1 && msg.Role == "user" {
			importance += 1000
		}

		tokenCount := msg.EstimateTokenCount()
		msgs[i] = msgWithTokens{
			index:      i,
			msg:        msg,
			tokenCount: tokenCount,
			importance: importance,
		}
	}

	// Ordenar por importância (maior primeiro)
	sort.Slice(msgs, func(i, j int) bool {
		return msgs[i].importance > msgs[j].importance
	})

	// Selecionar mensagens até atingir o limite, respeitando a ordem original
	selected := make(map[int]bool)
	currentTotal := 0

	for _, m := range msgs {
		if currentTotal+m.tokenCount <= maxTokens {
			selected[m.index] = true
			currentTotal += m.tokenCount
		} else if len(selected) == 0 {
			// Se ainda não selecionamos nenhuma mensagem, inclua pelo menos esta
			// mesmo que exceda o limite (será truncada depois)
			selected[m.index] = true
			break
		}
	}

	// Reconstruir o histórico na ordem original, apenas com as mensagens selecionadas
	var trimmedHistory []Message
	for i, msg := range history {
		if selected[i] {
			// Se for a última mensagem e ainda estamos acima do limite, truncá-la
			if i == len(history)-1 && currentTotal > maxTokens {
				exceededBy := currentTotal - maxTokens
				// Determinar quantos caracteres remover (aproximadamente)
				charsToRemove := int(float64(exceededBy) / 0.25)

				if len(msg.Content) > charsToRemove+100 {
					// Truncar a mensagem, mantendo o início
					msg.Content = msg.Content[:len(msg.Content)-charsToRemove] +
						"\n\n[...Conteúdo truncado para respeitar o limite de tokens...]"
				}
			}

			trimmedHistory = append(trimmedHistory, msg)
		}
	}

	return trimmedHistory
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
