package forwarder

type latestSummaryState struct {
	RuntimeSnapshot *ConversationFile
}

func (service *Service) loadLatestSummaryState(conversationID string) (*latestSummaryState, bool, error) {
	if service == nil || service.store == nil {
		return nil, false, nil
	}
	conversation, err := service.store.LoadConversation(conversationID)
	if err != nil || conversation == nil {
		return nil, false, err
	}
	return &latestSummaryState{RuntimeSnapshot: conversation}, true, nil
}

func (service *Service) loadLatestCarryForwardReplay(_ string) ([][]byte, bool, error) {
	return nil, false, nil
}

func (service *Service) loadLatestSummaryPromptTokens(conversationID string) (int64, bool, error) {
	if service == nil || service.store == nil {
		return 0, false, nil
	}
	conversation, err := service.store.LoadConversation(conversationID)
	if err != nil || conversation == nil {
		return 0, false, err
	}
	if conversation.AutoCompactionPromptTokens > 0 {
		return conversation.AutoCompactionPromptTokens, true, nil
	}
	if conversation.TokenDetailsUsedTokens > 0 {
		return int64(conversation.TokenDetailsUsedTokens), true, nil
	}
	return 0, false, nil
}
