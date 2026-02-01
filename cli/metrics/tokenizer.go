package metrics

import (
	"strings"

	"github.com/pkoukk/tiktoken-go"
)

// CountTokens conta tokens usando BPE (tiktoken) com fallback
func CountTokens(text, model string) int {
	if text == "" {
		return 0
	}
	model = strings.ToLower(model)

	tkm, err := tiktoken.EncodingForModel(model)
	if err != nil {
		// Fallback to gpt-4 if model not found
		tkm, err = tiktoken.EncodingForModel("gpt-4")
		if err != nil {
			// Final fallback: char estimation
			return len(text) / 4
		}
	}

	tokenized := tkm.Encode(text, nil, nil)
	return len(tokenized)
}
