package models

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ResponseData struct {
	Status   string `json:"status"`   // "processing", "completed", ou "error"
	Response string `json:"response"` // A resposta da LLM
	Message  string `json:"message"`  // Mensagem de erro, se houver
}
