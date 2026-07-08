package server

type ExecutionMode string

const (
	// ModeLocal 表示本地模式，适用于直接处理请求的情况。
	ModeLocal ExecutionMode = "local"
	// ModeUpstream 表示直连上游模式，适用于将请求转发到原始地址。
	ModeUpstream ExecutionMode = "upstream"
)

func parseExecutionMode(value string) ExecutionMode {
	switch value {
	case string(ModeUpstream):
		return ModeUpstream
	default:
		return ModeLocal
	}
}
