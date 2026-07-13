// Package virtualmodel 实现 Virtual Model Runtime（VMR），允许在 Cursor 中注册虚拟模型
//（如 MOA），这些模型在 Cursor 看来是普通模型，但实际上通过工作流编排多个物理模型
// 的调用链完成推理。
//
// 架构：
//
//	Cursor → Forwarder → VMR → Workflow → Nodes → Router → Provider → LLM
//
// 第一款内置虚拟模型：MOA（Multi-model Orchestration Architecture）。
package virtualmodel
