// bridge.go 实现 MVP 阶段的交互桥协议映射。
package interaction

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	neturl "net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	readability "codeberg.org/readeck/go-readability/v2"
	htmlmarkdown "github.com/firecrawl/html-to-markdown"
	mdplugin "github.com/firecrawl/html-to-markdown/plugin"

	"cursor/gen/agentv1"
	"cursor/internal/backend/agent/core"
	"cursor/internal/netproxy"
)

// InteractionApplyResult 表示一次交互桥结果归一化后的最小产物。
type InteractionApplyResult struct {
	// ToolCallID 表示该结果所属工具调用标识。
	ToolCallID string
	// InteractionID 表示该结果所属交互桥标识。
	InteractionID string
	// IsTerminal 表示交互桥是否已经收口。
	IsTerminal bool
	// ToolResultPayload 表示可继续喂给模型的结果摘要。
	ToolResultPayload string
	// ToolCall 保存可用于发 ToolCallCompletedUpdate 的工具调用对象。
	ToolCall *agentv1.ToolCall
}

// InteractionBridge 定义交互桥接口。
type InteractionBridge interface {
	// OpenQuery 打开一条交互型工具调用。
	OpenQuery(toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingInteraction, error)
	// ApplyInteractionResponse 处理交互响应。
	ApplyInteractionResponse(msg *agentv1.InteractionResponse, pending runtimecore.PendingInteraction) (InteractionApplyResult, error)
}

// Bridge 实现 MVP 阶段的交互桥。
type Bridge struct {
	// nextID 生成交互消息编号。
	nextID atomic.Uint32
	// httpClient 负责执行 web search / web fetch 等需要外网的操作。
	httpClient *http.Client
}

// NewBridge 创建一个交互桥实例。
func NewBridge() *Bridge {
	return &Bridge{
		httpClient: netproxy.NewHTTPClient(15 * time.Second),
	}
}

// OpenQuery 打开一条交互型工具调用。
func (bridge *Bridge) OpenQuery(toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingInteraction, error) {
	switch toolCall.ToolName {
	case "AskQuestion":
		return bridge.openAskQuestion(toolCall)
	case "CreatePlan":
		return bridge.openCreatePlan(toolCall)
	case "WebSearch":
		return bridge.openWebSearch(toolCall)
	case "WebFetch":
		return bridge.openWebFetch(toolCall)
	case "SwitchMode":
		return bridge.openSwitchMode(toolCall)
	default:
		return nil, runtimecore.PendingInteraction{}, fmt.Errorf("unsupported interaction tool: %s", toolCall.ToolName)
	}
}

// ApplyInteractionResponse 处理交互响应。
func (bridge *Bridge) ApplyInteractionResponse(msg *agentv1.InteractionResponse, pending runtimecore.PendingInteraction) (InteractionApplyResult, error) {
	if msg == nil {
		return InteractionApplyResult{}, fmt.Errorf("interaction response is required")
	}

	result := InteractionApplyResult{
		ToolCallID:    pending.ToolCallID,
		InteractionID: pending.InteractionID,
		IsTerminal:    true,
	}
	switch pending.InteractionKind {
	case "ask_question":
		var args agentv1.AskQuestionArgs
		_ = json.Unmarshal(pending.ArgsJSON, &args)
		result.ToolResultPayload = summarizeAskQuestionResponse(msg.GetAskQuestionInteractionResponse())
		result.ToolCall = &agentv1.ToolCall{
			Tool: &agentv1.ToolCall_AskQuestionToolCall{
				AskQuestionToolCall: &agentv1.AskQuestionToolCall{
					Args:   &args,
					Result: msg.GetAskQuestionInteractionResponse().GetResult(),
				},
			},
		}
		return result, nil
	case "create_plan":
		args, err := runtimecore.DecodeCreatePlanArgsJSON(pending.ArgsJSON)
		if err != nil {
			args = &agentv1.CreatePlanArgs{}
		}
		createPlanResult := normalizeCreatePlanResult(msg.GetCreatePlanRequestResponse())
		result.ToolResultPayload = summarizeCreatePlanResult(createPlanResult)
		result.ToolCall = &agentv1.ToolCall{
			Tool: &agentv1.ToolCall_CreatePlanToolCall{
				CreatePlanToolCall: &agentv1.CreatePlanToolCall{
					Args:   args,
					Result: createPlanResult,
				},
			},
		}
		return result, nil
	case "web_search":
		var args agentv1.WebSearchArgs
		_ = json.Unmarshal(pending.ArgsJSON, &args)
		webSearchResult, payload := bridge.applyWebSearchResponse(msg.GetWebSearchRequestResponse(), &args)
		result.ToolResultPayload = payload
		result.ToolCall = &agentv1.ToolCall{
			Tool: &agentv1.ToolCall_WebSearchToolCall{
				WebSearchToolCall: &agentv1.WebSearchToolCall{
					Args:   &args,
					Result: webSearchResult,
				},
			},
		}
		return result, nil
	case "web_fetch":
		var args agentv1.WebFetchArgs
		_ = json.Unmarshal(pending.ArgsJSON, &args)
		webFetchResult, payload := bridge.applyWebFetchResponse(msg.GetWebFetchRequestResponse(), &args)
		result.ToolResultPayload = payload
		result.ToolCall = &agentv1.ToolCall{
			Tool: &agentv1.ToolCall_WebFetchToolCall{
				WebFetchToolCall: &agentv1.WebFetchToolCall{
					Args:   &args,
					Result: webFetchResult,
				},
			},
		}
		return result, nil
	case "switch_mode":
		var args agentv1.SwitchModeArgs
		_ = json.Unmarshal(pending.ArgsJSON, &args)
		switchModeResult := buildSwitchModeResult(msg.GetSwitchModeRequestResponse(), &args)
		result.ToolResultPayload = summarizeSwitchModeResponse(switchModeResult)
		result.ToolCall = &agentv1.ToolCall{
			Tool: &agentv1.ToolCall_SwitchModeToolCall{
				SwitchModeToolCall: &agentv1.SwitchModeToolCall{
					Args:   &args,
					Result: switchModeResult,
				},
			},
		}
		return result, nil
	default:
		return InteractionApplyResult{}, fmt.Errorf("unsupported pending interaction kind: %s", pending.InteractionKind)
	}
}

// nextMessageID 返回下一个交互消息编号。
func (bridge *Bridge) nextMessageID() uint32 {
	current := bridge.nextID.Add(1)
	if current == 0 {
		current = bridge.nextID.Add(1)
	}
	return current
}

// openAskQuestion 构造 AskQuestion 交互查询。
func (bridge *Bridge) openAskQuestion(toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingInteraction, error) {
	var args agentv1.AskQuestionArgs
	if err := json.Unmarshal(toolCall.ArgsJSON, &args); err != nil {
		return nil, runtimecore.PendingInteraction{}, fmt.Errorf("decode AskQuestion args failed: %w", err)
	}
	messageID := bridge.nextMessageID()
	serverMessage := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_InteractionQuery{
			InteractionQuery: &agentv1.InteractionQuery{
				Id: messageID,
				Query: &agentv1.InteractionQuery_AskQuestionInteractionQuery{
					AskQuestionInteractionQuery: &agentv1.AskQuestionInteractionQuery{
						Args:       &args,
						ToolCallId: toolCall.CallID,
					},
				},
			},
		},
	}
	return serverMessage, runtimecore.PendingInteraction{
		InteractionID:   fmt.Sprintf("%d", messageID),
		ArgsJSON:        append([]byte(nil), toolCall.ArgsJSON...),
		ToolCallID:      toolCall.CallID,
		InteractionKind: "ask_question",
	}, nil
}

// openCreatePlan 构造 CreatePlan 交互查询。
func (bridge *Bridge) openCreatePlan(toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingInteraction, error) {
	args, err := runtimecore.DecodeCreatePlanArgsJSON(toolCall.ArgsJSON)
	if err != nil {
		return nil, runtimecore.PendingInteraction{}, fmt.Errorf("decode CreatePlan args failed: %w", err)
	}
	messageID := bridge.nextMessageID()
	serverMessage := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_InteractionQuery{
			InteractionQuery: &agentv1.InteractionQuery{
				Id: messageID,
				Query: &agentv1.InteractionQuery_CreatePlanRequestQuery{
					CreatePlanRequestQuery: &agentv1.CreatePlanRequestQuery{
						Args:       args,
						ToolCallId: toolCall.CallID,
					},
				},
			},
		},
	}
	return serverMessage, runtimecore.PendingInteraction{
		InteractionID:   fmt.Sprintf("%d", messageID),
		ArgsJSON:        append([]byte(nil), toolCall.ArgsJSON...),
		ToolCallID:      toolCall.CallID,
		InteractionKind: "create_plan",
	}, nil
}

// openWebSearch 构造 WebSearch 交互查询。
func (bridge *Bridge) openWebSearch(toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingInteraction, error) {
	var input struct {
		SearchTerm string `json:"search_term"`
	}
	if err := json.Unmarshal(toolCall.ArgsJSON, &input); err != nil {
		return nil, runtimecore.PendingInteraction{}, fmt.Errorf("decode WebSearch args failed: %w", err)
	}
	messageID := bridge.nextMessageID()
	serverMessage := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_InteractionQuery{
			InteractionQuery: &agentv1.InteractionQuery{
				Id: messageID,
				Query: &agentv1.InteractionQuery_WebSearchRequestQuery{
					WebSearchRequestQuery: &agentv1.WebSearchRequestQuery{
						Args: &agentv1.WebSearchArgs{
							SearchTerm: input.SearchTerm,
							ToolCallId: toolCall.CallID,
						},
					},
				},
			},
		},
	}
	return serverMessage, runtimecore.PendingInteraction{
		InteractionID:   fmt.Sprintf("%d", messageID),
		ArgsJSON:        append([]byte(nil), toolCall.ArgsJSON...),
		ToolCallID:      toolCall.CallID,
		InteractionKind: "web_search",
	}, nil
}

// openWebFetch 构造 WebFetch 交互查询。
func (bridge *Bridge) openWebFetch(toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingInteraction, error) {
	var input struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(toolCall.ArgsJSON, &input); err != nil {
		return nil, runtimecore.PendingInteraction{}, fmt.Errorf("decode WebFetch args failed: %w", err)
	}
	messageID := bridge.nextMessageID()
	serverMessage := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_InteractionQuery{
			InteractionQuery: &agentv1.InteractionQuery{
				Id: messageID,
				Query: &agentv1.InteractionQuery_WebFetchRequestQuery{
					WebFetchRequestQuery: &agentv1.WebFetchRequestQuery{
						Args: &agentv1.WebFetchArgs{
							Url:        input.URL,
							ToolCallId: toolCall.CallID,
						},
					},
				},
			},
		},
	}
	argsPayload, _ := json.Marshal(agentv1.WebFetchArgs{
		Url:        input.URL,
		ToolCallId: toolCall.CallID,
	})
	return serverMessage, runtimecore.PendingInteraction{
		InteractionID:   fmt.Sprintf("%d", messageID),
		ArgsJSON:        argsPayload,
		ToolCallID:      toolCall.CallID,
		InteractionKind: "web_fetch",
	}, nil
}

// openSwitchMode 构造 SwitchMode 交互查询。
func (bridge *Bridge) openSwitchMode(toolCall runtimecore.ToolInvocation) (*agentv1.AgentServerMessage, runtimecore.PendingInteraction, error) {
	var args agentv1.SwitchModeArgs
	if err := json.Unmarshal(toolCall.ArgsJSON, &args); err != nil {
		return nil, runtimecore.PendingInteraction{}, fmt.Errorf("decode SwitchMode args failed: %w", err)
	}
	if err := validateSwitchModeTargetID(args.GetTargetModeId()); err != nil {
		return nil, runtimecore.PendingInteraction{}, err
	}
	args.ToolCallId = toolCall.CallID
	messageID := bridge.nextMessageID()
	serverMessage := &agentv1.AgentServerMessage{
		Message: &agentv1.AgentServerMessage_InteractionQuery{
			InteractionQuery: &agentv1.InteractionQuery{
				Id: messageID,
				Query: &agentv1.InteractionQuery_SwitchModeRequestQuery{
					SwitchModeRequestQuery: &agentv1.SwitchModeRequestQuery{
						Args: &args,
					},
				},
			},
		},
	}
	argsPayload, _ := json.Marshal(args)
	return serverMessage, runtimecore.PendingInteraction{
		InteractionID:   fmt.Sprintf("%d", messageID),
		ArgsJSON:        argsPayload,
		ToolCallID:      toolCall.CallID,
		InteractionKind: "switch_mode",
	}, nil
}

func validateSwitchModeTargetID(raw string) error {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "agent", "ask", "plan":
		return nil
	default:
		return fmt.Errorf("unsupported target mode id: %q", strings.TrimSpace(raw))
	}
}

// summarizeAskQuestionResponse 生成 AskQuestion 响应摘要。
func summarizeAskQuestionResponse(response *agentv1.AskQuestionInteractionResponse) string {
	if response == nil || response.GetResult() == nil {
		return "ask question response missing"
	}
	switch item := response.GetResult().GetResult().(type) {
	case *agentv1.AskQuestionResult_Success:
		if len(item.Success.GetAnswers()) == 0 {
			return "ask question success"
		}
		return fmt.Sprintf("ask question answers=%d", len(item.Success.GetAnswers()))
	case *agentv1.AskQuestionResult_Error:
		return item.Error.GetErrorMessage()
	case *agentv1.AskQuestionResult_Rejected:
		return item.Rejected.GetReason()
	case *agentv1.AskQuestionResult_Async:
		return "ask question async accepted"
	default:
		return "unknown ask question response"
	}
}

const createPlanEmptyURIError = "create plan failed: Cursor returned success with empty planUri"

// normalizeCreatePlanResult 兜底客户端 success 但未返回 planUri 的异常形态。
func normalizeCreatePlanResult(response *agentv1.CreatePlanRequestResponse) *agentv1.CreatePlanResult {
	if response == nil || response.GetResult() == nil {
		return nil
	}
	result := response.GetResult()
	if result.GetSuccess() != nil && strings.TrimSpace(result.GetPlanUri()) == "" {
		return &agentv1.CreatePlanResult{
			Result: &agentv1.CreatePlanResult_Error{
				Error: &agentv1.CreatePlanError{Error: createPlanEmptyURIError},
			},
		}
	}
	return result
}

// summarizeCreatePlanResult 生成 CreatePlan 响应摘要。
func summarizeCreatePlanResult(result *agentv1.CreatePlanResult) string {
	if result == nil {
		return "create plan response missing"
	}
	switch item := result.GetResult().(type) {
	case *agentv1.CreatePlanResult_Success:
		return fmt.Sprintf("create plan success uri=%s", result.GetPlanUri())
	case *agentv1.CreatePlanResult_Error:
		return item.Error.GetError()
	default:
		return "unknown create plan response"
	}
}

// summarizeWebSearchResponse 生成 WebSearch 响应摘要。
func summarizeWebSearchResponse(response *agentv1.WebSearchRequestResponse) string {
	if response == nil {
		return "web search response missing"
	}
	switch item := response.GetResult().(type) {
	case *agentv1.WebSearchRequestResponse_Approved_:
		_ = item
		return "web search approved"
	case *agentv1.WebSearchRequestResponse_Rejected_:
		return item.Rejected.GetReason()
	default:
		return "unknown web search response"
	}
}

// applyWebSearchResponse 把 WebSearch approval 响应转换成最终工具结果。
func (bridge *Bridge) applyWebSearchResponse(response *agentv1.WebSearchRequestResponse, args *agentv1.WebSearchArgs) (*agentv1.WebSearchResult, string) {
	if response == nil {
		return &agentv1.WebSearchResult{
			Result: &agentv1.WebSearchResult_Error{
				Error: &agentv1.WebSearchError{Error: "web search response missing"},
			},
		}, "web search response missing"
	}
	switch item := response.GetResult().(type) {
	case *agentv1.WebSearchRequestResponse_Approved_:
		_ = item
		references, payload, err := bridge.executeWebSearch(strings.TrimSpace(args.GetSearchTerm()))
		if err != nil {
			return &agentv1.WebSearchResult{
				Result: &agentv1.WebSearchResult_Error{
					Error: &agentv1.WebSearchError{Error: err.Error()},
				},
			}, err.Error()
		}
		references, payload = truncateWebSearchReplay(strings.TrimSpace(args.GetSearchTerm()), references, payload)
		return &agentv1.WebSearchResult{
			Result: &agentv1.WebSearchResult_Success{
				Success: &agentv1.WebSearchSuccess{References: references},
			},
		}, payload
	case *agentv1.WebSearchRequestResponse_Rejected_:
		return &agentv1.WebSearchResult{
			Result: &agentv1.WebSearchResult_Rejected{
				Rejected: &agentv1.WebSearchRejected{Reason: item.Rejected.GetReason()},
			},
		}, item.Rejected.GetReason()
	default:
		return &agentv1.WebSearchResult{
			Result: &agentv1.WebSearchResult_Error{
				Error: &agentv1.WebSearchError{Error: "unknown web search response"},
			},
		}, "unknown web search response"
	}
}

// applyWebFetchResponse 把 WebFetch approval 响应转换成最终工具结果。
func (bridge *Bridge) applyWebFetchResponse(response *agentv1.WebFetchRequestResponse, args *agentv1.WebFetchArgs) (*agentv1.WebFetchResult, string) {
	if response == nil {
		return &agentv1.WebFetchResult{
			Result: &agentv1.WebFetchResult_Error{
				Error: &agentv1.WebFetchError{
					Url:   args.GetUrl(),
					Error: "web fetch response missing",
				},
			},
		}, "web fetch response missing"
	}
	switch item := response.GetResult().(type) {
	case *agentv1.WebFetchRequestResponse_Approved_:
		_ = item
		markdown, err := bridge.executeWebFetch(strings.TrimSpace(args.GetUrl()))
		if err != nil {
			return &agentv1.WebFetchResult{
				Result: &agentv1.WebFetchResult_Error{
					Error: &agentv1.WebFetchError{
						Url:   args.GetUrl(),
						Error: err.Error(),
					},
				},
			}, err.Error()
		}
		return &agentv1.WebFetchResult{
			Result: &agentv1.WebFetchResult_Success{
				Success: &agentv1.WebFetchSuccess{
					Url:      args.GetUrl(),
					Markdown: markdown,
				},
			},
		}, markdown
	case *agentv1.WebFetchRequestResponse_Rejected_:
		return &agentv1.WebFetchResult{
			Result: &agentv1.WebFetchResult_Rejected{
				Rejected: &agentv1.WebFetchRejected{Reason: item.Rejected.GetReason()},
			},
		}, item.Rejected.GetReason()
	default:
		return &agentv1.WebFetchResult{
			Result: &agentv1.WebFetchResult_Error{
				Error: &agentv1.WebFetchError{
					Url:   args.GetUrl(),
					Error: "unknown web fetch response",
				},
			},
		}, "unknown web fetch response"
	}
}

// buildSwitchModeResult 把 SwitchMode approval 响应转换成最终工具结果。
func buildSwitchModeResult(response *agentv1.SwitchModeRequestResponse, args *agentv1.SwitchModeArgs) *agentv1.SwitchModeResult {
	if response == nil {
		return &agentv1.SwitchModeResult{
			Result: &agentv1.SwitchModeResult_Error{
				Error: &agentv1.SwitchModeError{Error: "switch mode response missing"},
			},
		}
	}
	switch item := response.GetResult().(type) {
	case *agentv1.SwitchModeRequestResponse_Approved_:
		_ = item
		targetModeID := strings.ToLower(strings.TrimSpace(args.GetTargetModeId()))
		return &agentv1.SwitchModeResult{
			Result: &agentv1.SwitchModeResult_Success{
				Success: &agentv1.SwitchModeSuccess{
					FromModeId: "unknown",
					ToModeId:   targetModeID,
				},
			},
		}
	case *agentv1.SwitchModeRequestResponse_Rejected_:
		return &agentv1.SwitchModeResult{
			Result: &agentv1.SwitchModeResult_Rejected{
				Rejected: &agentv1.SwitchModeRejected{Reason: item.Rejected.GetReason()},
			},
		}
	default:
		return &agentv1.SwitchModeResult{
			Result: &agentv1.SwitchModeResult_Error{
				Error: &agentv1.SwitchModeError{Error: "unknown switch mode response"},
			},
		}
	}
}

// summarizeSwitchModeResponse 生成 SwitchMode 响应摘要。
func summarizeSwitchModeResponse(result *agentv1.SwitchModeResult) string {
	if result == nil {
		return "switch mode result missing"
	}
	switch item := result.GetResult().(type) {
	case *agentv1.SwitchModeResult_Success:
		return fmt.Sprintf("switch mode success to=%s", item.Success.GetToModeId())
	case *agentv1.SwitchModeResult_Rejected:
		return item.Rejected.GetReason()
	case *agentv1.SwitchModeResult_Error:
		return item.Error.GetError()
	default:
		return "unknown switch mode result"
	}
}

var (
	webSearchAnchorPattern  = regexp.MustCompile(`(?is)<a[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	webSearchSnippetPattern = regexp.MustCompile(`(?is)<(?:a|div)[^>]*class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</(?:a|div)>`)
	htmlTitlePattern        = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	htmlTagPattern          = regexp.MustCompile(`(?is)<[^>]+>`)
	webSearchURLOverride    = "https://html.duckduckgo.com/html/?q="
)

const (
	webFetchBodyLimit     = 2 * 1024 * 1024
	webFetchMarkdownLimit = 32 * 1024
	webSearchPayloadLimit = 16 * 1024
	webSearchTitleLimit   = 512
	webSearchChunkLimit   = 2 * 1024
)

func (bridge *Bridge) executeWebSearch(searchTerm string) ([]*agentv1.WebSearchReference, string, error) {
	if strings.TrimSpace(searchTerm) == "" {
		return nil, "", fmt.Errorf("web search search_term is required")
	}
	client := bridge.httpClient
	if client == nil {
		client = netproxy.NewHTTPClient(15 * time.Second)
	}
	requestURL := webSearchURLOverride + neturl.QueryEscape(searchTerm)
	request, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("User-Agent", "cursor-local-agent/1.0")
	response, err := client.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, "", fmt.Errorf("web search http status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	if err != nil {
		return nil, "", err
	}
	references := extractWebSearchReferences(string(body))
	if len(references) == 0 {
		return nil, "", fmt.Errorf("web search returned no parseable results")
	}
	if len(references) > 5 {
		references = references[:5]
	}
	return references, formatWebSearchPayload(searchTerm, references), nil
}

func extractWebSearchReferences(body string) []*agentv1.WebSearchReference {
	anchorMatches := webSearchAnchorPattern.FindAllStringSubmatch(body, 8)
	snippetMatches := webSearchSnippetPattern.FindAllStringSubmatch(body, 8)
	references := make([]*agentv1.WebSearchReference, 0, len(anchorMatches))
	for index, match := range anchorMatches {
		if len(match) < 3 {
			continue
		}
		title := cleanupWebSearchHTML(match[2])
		url := strings.TrimSpace(html.UnescapeString(match[1]))
		snippet := ""
		if index < len(snippetMatches) && len(snippetMatches[index]) >= 2 {
			snippet = cleanupWebSearchHTML(snippetMatches[index][1])
		}
		if title == "" || url == "" {
			continue
		}
		references = append(references, &agentv1.WebSearchReference{
			Title: title,
			Url:   url,
			Chunk: snippet,
		})
	}
	return references
}

func cleanupWebSearchHTML(value string) string {
	withoutTags := htmlTagPattern.ReplaceAllString(value, " ")
	unescaped := html.UnescapeString(withoutTags)
	return strings.Join(strings.Fields(unescaped), " ")
}

func formatWebSearchPayload(searchTerm string, references []*agentv1.WebSearchReference) string {
	lines := []string{
		fmt.Sprintf("Title: Web search results for query: %s", strings.TrimSpace(searchTerm)),
		"Content: Links:",
	}
	for index, reference := range references {
		if reference == nil {
			continue
		}
		lines = append(lines, fmt.Sprintf("%d. [%s](%s)", index+1, strings.TrimSpace(reference.GetTitle()), strings.TrimSpace(reference.GetUrl())))
	}
	snippets := make([]string, 0, len(references))
	for _, reference := range references {
		if reference == nil {
			continue
		}
		chunk := strings.TrimSpace(reference.GetChunk())
		if chunk == "" {
			continue
		}
		snippets = append(snippets, fmt.Sprintf("- %s", chunk))
	}
	if len(snippets) > 0 {
		lines = append(lines, "", strings.Join(snippets, "\n"))
	}
	return strings.Join(lines, "\n")
}

func truncateWebSearchReplay(searchTerm string, references []*agentv1.WebSearchReference, payload string) ([]*agentv1.WebSearchReference, string) {
	truncated := false
	nextReferences := make([]*agentv1.WebSearchReference, 0, len(references))
	for _, reference := range references {
		if reference == nil {
			continue
		}
		next := *reference
		title := truncateInteractionText("WebSearch title", next.GetTitle(), webSearchTitleLimit)
		chunk := truncateInteractionText("WebSearch snippet", next.GetChunk(), webSearchChunkLimit)
		if title != next.GetTitle() || chunk != next.GetChunk() {
			truncated = true
		}
		next.Title = title
		next.Chunk = chunk
		nextReferences = append(nextReferences, &next)
	}
	nextPayload := formatWebSearchPayload(searchTerm, nextReferences)
	if strings.TrimSpace(payload) != "" && len(nextPayload) == 0 {
		nextPayload = payload
	}
	if len(nextPayload) > webSearchPayloadLimit {
		truncated = true
		nextPayload = truncateInteractionText("WebSearch", nextPayload, webSearchPayloadLimit)
	}
	if truncated && len(nextReferences) > 0 {
		last := nextReferences[len(nextReferences)-1]
		last.Chunk = strings.TrimSpace(last.GetChunk() + "\n\n" + interactionTruncationNotice("WebSearch", webSearchPayloadLimit, len(nextPayload), len(payload)))
		nextPayload = formatWebSearchPayload(searchTerm, nextReferences)
		nextPayload = truncateInteractionText("WebSearch", nextPayload, webSearchPayloadLimit)
	}
	return nextReferences, nextPayload
}

func (bridge *Bridge) executeWebFetch(rawURL string) (string, error) {
	parsedURL, err := validateWebFetchURL(rawURL)
	if err != nil {
		return "", err
	}
	client := bridge.httpClient
	if client == nil {
		client = netproxy.NewHTTPClient(15 * time.Second)
	}
	client = webFetchHTTPClient(client)
	request, err := http.NewRequest(http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "cursor-local-agent/1.0")
	request.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/xml,application/json;q=0.9,*/*;q=0.1")
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("web fetch http status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, webFetchBodyLimit+1))
	if err != nil {
		return "", err
	}
	if len(body) == 0 {
		return "", fmt.Errorf("web fetch returned empty body")
	}
	if len(body) > webFetchBodyLimit {
		body = body[:webFetchBodyLimit]
	}
	contentType := response.Header.Get("Content-Type")
	if contentType == "" {
		contentType = http.DetectContentType(body)
	}
	if !isWebFetchTextContentType(contentType) {
		return "", fmt.Errorf("web fetch unsupported content type %q", contentType)
	}
	markdown, title, err := renderWebFetchMarkdown(parsedURL, body, contentType)
	if err != nil {
		return "", err
	}
	markdown = strings.TrimSpace(markdown)
	if markdown == "" {
		return "", fmt.Errorf("web fetch returned empty markdown")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = parsedURL.String()
	}
	payload := fmt.Sprintf("Title: %s\nURL: %s\n\nContent:\n%s", title, parsedURL.String(), markdown)
	return truncateWebFetchMarkdown(payload), nil
}

func validateWebFetchURL(rawURL string) (*neturl.URL, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, fmt.Errorf("web fetch url is required")
	}
	parsedURL, err := neturl.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("web fetch invalid url: %w", err)
	}
	switch strings.ToLower(parsedURL.Scheme) {
	case "http", "https":
	default:
		return nil, fmt.Errorf("web fetch only supports http and https urls")
	}
	host := strings.TrimSpace(parsedURL.Hostname())
	if host == "" {
		return nil, fmt.Errorf("web fetch url host is required")
	}
	if isBlockedWebFetchHost(host) {
		return nil, fmt.Errorf("web fetch host is not public-web accessible")
	}
	return parsedURL, nil
}

func isBlockedWebFetchHost(host string) bool {
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified()
}

func isWebFetchTextContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	}
	if strings.HasPrefix(mediaType, "text/") {
		return true
	}
	switch mediaType {
	case "application/xhtml+xml", "application/xml", "application/json", "application/ld+json", "application/rss+xml", "application/atom+xml":
		return true
	default:
		return strings.HasSuffix(mediaType, "+xml") || strings.HasSuffix(mediaType, "+json")
	}
}

func renderWebFetchMarkdown(pageURL *neturl.URL, body []byte, contentType string) (string, string, error) {
	if !isHTMLLikeContentType(contentType) {
		return string(body), "", nil
	}
	article, err := readability.FromReader(bytes.NewReader(body), pageURL)
	if err == nil {
		var articleHTML bytes.Buffer
		if renderErr := article.RenderHTML(&articleHTML); renderErr == nil && strings.TrimSpace(articleHTML.String()) != "" {
			if markdown, convertErr := convertHTMLToMarkdown(pageURL, articleHTML.String()); convertErr == nil && strings.TrimSpace(markdown) != "" {
				return markdown, article.Title(), nil
			}
		}
	}
	markdown, err := convertHTMLToMarkdown(pageURL, string(body))
	if err != nil {
		return "", "", fmt.Errorf("web fetch markdown conversion failed: %w", err)
	}
	return markdown, extractWebFetchHTMLTitle(string(body)), nil
}

func isHTMLLikeContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	}
	return mediaType == "text/html" || mediaType == "application/xhtml+xml" || mediaType == ""
}

func convertHTMLToMarkdown(pageURL *neturl.URL, htmlBody string) (string, error) {
	converter := htmlmarkdown.NewConverter(htmlmarkdown.DomainFromURL(pageURL.String()), true, nil)
	converter.Use(mdplugin.GitHubFlavored())
	return converter.ConvertString(htmlBody)
}

func extractWebFetchHTMLTitle(htmlBody string) string {
	matches := htmlTitlePattern.FindStringSubmatch(htmlBody)
	if len(matches) < 2 {
		return ""
	}
	return cleanupWebSearchHTML(matches[1])
}

func truncateWebFetchMarkdown(markdown string) string {
	return truncateInteractionText("WebFetch", markdown, webFetchMarkdownLimit)
}

func truncateInteractionText(toolName string, text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	original := len(text)
	notice := fmt.Sprintf("\n\n%s", interactionTruncationNotice(toolName, limit, limit, original))
	for {
		keep := limit - len(notice)
		if keep <= 0 {
			return truncateInteractionUTF8(text, limit)
		}
		kept := truncateInteractionUTF8(text, keep)
		nextNotice := fmt.Sprintf("\n\n%s", interactionTruncationNotice(toolName, limit, len(kept), original))
		output := strings.TrimRight(kept, "\n") + nextNotice
		if len(output) <= limit || nextNotice == notice {
			return output
		}
		notice = nextNotice
	}
}

func interactionTruncationNotice(toolName string, limit int, kept int, original int) string {
	return fmt.Sprintf("[truncated: %s result exceeded %d bytes; showing %d of %d bytes]", toolName, limit, kept, original)
}

func truncateInteractionUTF8(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	if limit > len(text) {
		limit = len(text)
	}
	truncated := text[:limit]
	for !utf8.ValidString(truncated) && len(truncated) > 0 {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated
}

func webFetchHTTPClient(base *http.Client) *http.Client {
	if base == nil {
		base = netproxy.NewHTTPClient(15 * time.Second)
	}
	client := *base
	previousCheckRedirect := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("web fetch stopped after 10 redirects")
		}
		if _, err := validateWebFetchURL(request.URL.String()); err != nil {
			return err
		}
		if previousCheckRedirect != nil {
			return previousCheckRedirect(request, via)
		}
		return nil
	}
	return &client
}
