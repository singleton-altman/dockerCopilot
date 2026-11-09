package mcp

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/onlyLTY/dockerCopilot/internal/svc"

	"github.com/zeromicro/go-zero/core/logx"
)

// Handler MCP HTTP 端点。实现 MCP 的 Streamable HTTP 传输：
// 客户端 POST JSON-RPC 请求，服务端同步返回 JSON-RPC 响应，不使用 SSE 流。
type Handler struct {
	svcCtx   *svc.ServiceContext
	registry *Registry
	manager  *Manager
	version  string
	maxBody  int64
}

// NewHandler 创建 MCP 端点。开关与密钥从 manager 运行时读取，
// 未启用或密钥为空时拒绝所有请求（安全默认），配置修改热生效无需重启。
func NewHandler(svcCtx *svc.ServiceContext, manager *Manager, version string) *Handler {
	return &Handler{
		svcCtx:   svcCtx,
		registry: NewRegistry(svcCtx),
		manager:  manager,
		version:  version,
		// 写 compose 文件可能带上完整 YAML，4 MiB 足够且限制滥用。
		maxBody: 4 << 20,
	}
}

// ServeHTTP 处理 POST /mcp。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.manager.Enabled() {
		// 端点固定注册，关闭时以 404 表达"服务不存在"。
		http.Error(w, "MCP service disabled", http.StatusNotFound)
		return
	}
	if !h.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="docker-copilot-mcp"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodPost:
		// 仅支持 JSON 响应；即使客户端声明接受 SSE，也按 JSON 返回。
		h.handlePost(w, r)
	case http.MethodGet:
		// Streamable HTTP 允许 GET 建 SSE 流，本实现不提供长连接。
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed; use POST", http.StatusMethodNotAllowed)
	default:
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// authorized 校验 Bearer 密钥。用常量时间比较，避免时序侧信道。
func (h *Handler) authorized(r *http.Request) bool {
	secret := h.manager.AccessKey()
	if secret == "" {
		return false
	}
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(secret)) == 1
}

func (h *Handler) handlePost(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, h.maxBody))
	if err != nil {
		h.writeError(w, nil, CodeParseError, "读取请求体失败")
		return
	}
	if len(body) == 0 {
		h.writeError(w, nil, CodeParseError, "请求体为空")
		return
	}

	// 支持单条请求，也支持批量（JSON 数组）。
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "[") {
		var batch []Request
		if err := json.Unmarshal(body, &batch); err != nil {
			h.writeError(w, nil, CodeParseError, "请求体不是合法 JSON")
			return
		}
		responses := make([]Response, 0, len(batch))
		for i := range batch {
			if resp, ok := h.dispatch(&batch[i]); ok {
				responses = append(responses, resp)
			}
		}
		if len(responses) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		h.writeJSON(w, responses)
		return
	}

	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		h.writeError(w, nil, CodeParseError, "请求体不是合法 JSON")
		return
	}
	resp, ok := h.dispatch(&req)
	if !ok {
		// 通知无需响应。
		w.WriteHeader(http.StatusAccepted)
		return
	}
	h.writeJSON(w, resp)
}

// dispatch 分发单条请求。返回 (响应, 是否需要回响应)。
func (h *Handler) dispatch(req *Request) (Response, bool) {
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		return errorResponse(req.ID, CodeInvalidRequest, "仅支持 JSON-RPC 2.0"), true
	}

	switch req.Method {
	case "initialize":
		var params InitializeParams
		_ = json.Unmarshal(req.Params, &params)
		version := params.ProtocolVersion
		// 客户端版本不在支持列表时回落到兼容版本，避免握手失败。
		if version == "" || version != ProtocolVersion {
			if version == FallbackProtocol {
				version = FallbackProtocol
			} else {
				version = ProtocolVersion
			}
		}
		return okResponse(req.ID, InitializeResult{
			ProtocolVersion: version,
			Capabilities:    Capabilities{Tools: &ToolsCapability{ListChanged: false}},
			ServerInfo: ServerInfo{
				Name:    serverName,
				Title:   serverTitle,
				Version: h.version,
			},
			Instructions: instructions,
		}), true

	case "notifications/initialized", "notifications/cancelled":
		return Response{}, false

	case "ping":
		return okResponse(req.ID, map[string]any{}), true

	case "tools/list":
		return okResponse(req.ID, ToolsListResult{Tools: h.registry.Tools()}), true

	case "tools/call":
		var params ToolCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return errorResponse(req.ID, CodeInvalidParams, "参数格式错误"), true
		}
		if params.Name == "" {
			return errorResponse(req.ID, CodeInvalidParams, "缺少工具名"), true
		}
		result := h.registry.Call(reqContext(req), params.Name, params.Arguments)
		return okResponse(req.ID, result), true

	default:
		return errorResponse(req.ID, CodeMethodNotFound, "不支持的方法："+req.Method), true
	}
}

// reqContext 用请求上下文（当前实现不额外注入超时，交给 HTTP 层）。
func reqContext(_ *Request) context.Context {
	return context.Background()
}

func okResponse(id json.RawMessage, result any) Response {
	return Response{JSONRPC: "2.0", ID: id, Result: result}
}

func errorResponse(id json.RawMessage, code int, message string) Response {
	return Response{JSONRPC: "2.0", ID: id, Error: NewError(code, message)}
}

func (h *Handler) writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		logx.Errorf("mcp: 写响应失败: %v", err)
	}
}

func (h *Handler) writeError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	// JSON-RPC 层错误仍返回 HTTP 200，由 error 字段表达。
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(errorResponse(id, code, message))
}

// instructions 在 initialize 时发给客户端，说明边界与推荐用法。
const instructions = `Docker Copilot MCP 服务：管理本机 Docker 容器与 compose 编排。

使用建议：
1. 修改 compose 前先 read_compose_file 读取原内容，改动后先 validate_compose_file 再 write_compose_file。
2. deploy_compose 是异步的，返回 jobID 后必须用 get_compose_job 轮询到 done=true；status=interrupted 表示结果不确定，需人工核验，不要自动重试。
3. stop_container / deploy_compose 会影响线上服务，执行前请与用户确认。
4. 不要把容器日志或 compose 内容中的密钥、密码回显给用户。`
