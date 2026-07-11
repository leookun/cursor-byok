package forwarder

type streamEditLogRecorder struct{}

func newStreamEditLogRecorder(_ string, _ string) (*streamEditLogRecorder, error) {
	return &streamEditLogRecorder{}, nil
}

func (recorder *streamEditLogRecorder) RecordLLMRequest(_ string, _ string, _ string, _ map[string]any) (string, error) {
	return "", nil
}

func (recorder *streamEditLogRecorder) AppendLLMResponseChunk(_ string, _ string, _ string, _ string) (string, error) {
	return "", nil
}

func (recorder *streamEditLogRecorder) RecordLLMSummary(_ string, _ string, _ string, _ map[string]any) (string, error) {
	return "", nil
}

func (recorder *streamEditLogRecorder) appendEvent(_ string, _ map[string]any) (string, error) {
	return "", nil
}
