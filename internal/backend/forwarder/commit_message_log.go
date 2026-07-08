package forwarder

type commitMessageLogRecorder struct{}

func newCommitMessageLogRecorder(_ string, _ string) (*commitMessageLogRecorder, error) {
	return &commitMessageLogRecorder{}, nil
}

func (recorder *commitMessageLogRecorder) RecordLLMRequest(_ string, _ string, _ string, _ map[string]any) (string, error) {
	return "", nil
}

func (recorder *commitMessageLogRecorder) AppendLLMResponseChunk(_ string, _ string, _ string, _ string) (string, error) {
	return "", nil
}

func (recorder *commitMessageLogRecorder) RecordLLMSummary(_ string, _ string, _ string, _ map[string]any) (string, error) {
	return "", nil
}

func (recorder *commitMessageLogRecorder) appendEvent(_ string, _ map[string]any) (string, error) {
	return "", nil
}
