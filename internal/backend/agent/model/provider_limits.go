package modeladapter

func maxAnthropicTokens(req StreamRequest) int {
	if req.AnthropicMaxTokens > 0 {
		return req.AnthropicMaxTokens
	}
	if req.MaxTokens > 0 {
		return req.MaxTokens
	}
	return 65536
}

func maxThinkingBudget(req StreamRequest) int {
	if req.ThinkingBudgetTokens > 0 {
		return req.ThinkingBudgetTokens
	}
	return anthropicThinkingBudget(maxAnthropicTokens(req))
}
