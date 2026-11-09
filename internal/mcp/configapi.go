package mcp

import (
	"encoding/json"
	"net/http"

	"github.com/zeromicro/go-zero/rest/httpx"
)

// mcpEndpointPath MCP 端点路径，随配置一起下发给前端拼接完整 URL。
const mcpEndpointPath = "/mcp"

// ConfigAPI MCP 运行时配置的 HTTP 接口（JWT 保护，由路由注册方附加）。
// 依赖注入 Manager 而非 svc.ServiceContext，避免 import cycle。
type ConfigAPI struct {
	manager *Manager
}

// NewConfigAPI 创建配置接口。
func NewConfigAPI(manager *Manager) *ConfigAPI {
	return &ConfigAPI{manager: manager}
}

func (api *ConfigAPI) response() map[string]interface{} {
	cfg := api.manager.Current()
	return map[string]interface{}{
		"enabled":   cfg.Enabled,
		"accessKey": cfg.AccessKey,
		"endpoint":  mcpEndpointPath,
	}
}

// GetHandler GET /api/mcp/config：返回 MCP 当前配置。
// 登录态与 Web/App 共用，明文回传密钥供用户配置 AI 客户端。
func (api *ConfigAPI) GetHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httpx.OkJsonCtx(r.Context(), w, map[string]interface{}{
			"code": 200,
			"msg":  "success",
			"data": api.response(),
		})
	}
}

type mcpConfigReq struct {
	Enabled   bool   `json:"enabled"`
	AccessKey string `json:"accessKey"`
}

// UpdateHandler POST /api/mcp/config：更新开关与密钥，
// 热生效并持久化（Manager 负责），无需重启。
func (api *ConfigAPI) UpdateHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req mcpConfigReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.OkJsonCtx(r.Context(), w, map[string]interface{}{
				"code": 400,
				"msg":  "请求体不是合法 JSON",
				"data": map[string]interface{}{},
			})
			return
		}
		if err := api.manager.Update(req.Enabled, req.AccessKey); err != nil {
			httpx.OkJsonCtx(r.Context(), w, map[string]interface{}{
				"code": 400,
				"msg":  err.Error(),
				"data": map[string]interface{}{},
			})
			return
		}
		httpx.OkJsonCtx(r.Context(), w, map[string]interface{}{
			"code": 200,
			"msg":  "success",
			"data": api.response(),
		})
	}
}
