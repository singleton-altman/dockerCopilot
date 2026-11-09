// Package mcp 提供 Model Context Protocol（MCP）服务端实现，
// 让 AI 客户端（Claude Desktop、Cursor 等）可以直接读取和操作 Docker Copilot。
//
// 传输：HTTP Streamable（JSON-RPC 2.0 over POST /mcp），无 SSE 长连接，
// 便于在 NAS 后面直接暴露；鉴权用独立密钥（MCP_SECRET_KEY），
// 不与会话 JWT 混用，避免把长期凭据写进 AI 客户端配置。
package mcp

import (
	"encoding/json"
	"fmt"
)

// JSON-RPC 2.0 错误码
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Request 一条 JSON-RPC 请求（通知时无 ID）。
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response JSON-RPC 响应。Result 与 Error 互斥。
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError JSON-RPC 错误体。
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("mcp: %d %s", e.Code, e.Message) }

// NewError 构造 RPC 错误。
func NewError(code int, message string) *RPCError {
	return &RPCError{Code: code, Message: message}
}

// IsNotification 通知没有 id，服务端不应回响应。
func (r *Request) IsNotification() bool {
	return len(r.ID) == 0
}

// 协议版本：优先使用客户端请求的版本，未请求时用本实现支持的版本。
const (
	ProtocolVersion  = "2025-06-18"
	FallbackProtocol = "2024-11-05"
	serverName       = "docker-copilot"
	serverTitle      = "Docker Copilot"
)

// ServerInfo MCP initialize 响应中的 serverInfo。
type ServerInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

// Capabilities 声明本服务支持的能力：只有 tools。
type Capabilities struct {
	Tools *ToolsCapability `json:"tools,omitempty"`
}

type ToolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

// InitializeResult initialize 方法的响应。
type InitializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    Capabilities `json:"capabilities"`
	ServerInfo      ServerInfo   `json:"serverInfo"`
	Instructions    string       `json:"instructions,omitempty"`
}

// Tool MCP 工具定义。
type Tool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	// Annotations 提示客户端这是只读还是破坏性操作（MCP 2025-06-18）。
	Annotations map[string]any `json:"annotations,omitempty"`
}

// ToolsListResult tools/list 的响应。
type ToolsListResult struct {
	Tools []Tool `json:"tools"`
}

// Content 工具返回内容块。
type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ToolCallResult tools/call 的响应。IsError 表示工具执行失败（协议层仍成功）。
type ToolCallResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// TextResult 构造纯文本结果。
func TextResult(text string) *ToolCallResult {
	return &ToolCallResult{Content: []Content{{Type: "text", Text: text}}}
}

// ErrorResult 构造工具级错误结果。
func ErrorResult(text string) *ToolCallResult {
	return &ToolCallResult{Content: []Content{{Type: "text", Text: text}}, IsError: true}
}

// ToolCallParams tools/call 的入参。
type ToolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// InitializeParams initialize 的入参。
type InitializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      map[string]any `json:"clientInfo"`
}

// 只读 / 修改类注解，供 tools/list 提供给客户端做授权提示。
func readOnlyAnnotations() map[string]any {
	return map[string]any{"readOnlyHint": true, "openWorldHint": false}
}

func mutateAnnotations(destructive bool) map[string]any {
	return map[string]any{
		"readOnlyHint":    false,
		"destructiveHint": destructive,
		"idempotentHint":  false,
		"openWorldHint":   false,
	}
}
