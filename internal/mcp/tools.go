package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	composelogic "github.com/onlyLTY/dockerCopilot/internal/logic/composefile"
	containerlogic "github.com/onlyLTY/dockerCopilot/internal/logic/container"
	imagelogic "github.com/onlyLTY/dockerCopilot/internal/logic/image"
	"github.com/onlyLTY/dockerCopilot/internal/svc"
	"github.com/onlyLTY/dockerCopilot/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

// Registry 持有工具表。所有工具复用 HTTP API 的 logic 层，
// 保证 MCP 与 Web/App 的行为、错误口径一致。
type Registry struct {
	svcCtx *svc.ServiceContext
}

func NewRegistry(svcCtx *svc.ServiceContext) *Registry {
	return &Registry{svcCtx: svcCtx}
}

// Tools 返回工具清单。写入类工具带 destructiveHint，
// 客户端可据此向用户提示风险。
func (r *Registry) Tools() []Tool {
	return []Tool{
		{
			Name:        "list_containers",
			Title:       "列出容器",
			Description: "列出所有容器（含已停止），返回名称、状态、镜像、是否可更新、compose 项目等。",
			InputSchema: objectSchema(nil),
			Annotations: readOnlyAnnotations(),
		},
		{
			Name:        "container_logs",
			Title:       "查看容器日志",
			Description: "读取指定容器的日志。id 可传容器 ID 或名称。",
			InputSchema: objectSchema(map[string]any{
				"id":         stringProp("容器 ID 或名称"),
				"tail":       map[string]any{"type": "integer", "description": "返回末尾多少行，默认 200", "default": 200},
				"timestamps": map[string]any{"type": "boolean", "description": "是否带时间戳", "default": false},
			}, "id"),
			Annotations: readOnlyAnnotations(),
		},
		{
			Name:        "start_container",
			Title:       "启动容器",
			Description: "启动指定容器。id 可传容器 ID 或名称。",
			InputSchema: objectSchema(map[string]any{"id": stringProp("容器 ID 或名称")}, "id"),
			Annotations: mutateAnnotations(false),
		},
		{
			Name:        "stop_container",
			Title:       "停止容器",
			Description: "停止指定容器。会中断服务，请先确认影响。",
			InputSchema: objectSchema(map[string]any{"id": stringProp("容器 ID 或名称")}, "id"),
			Annotations: mutateAnnotations(true),
		},
		{
			Name:        "restart_container",
			Title:       "重启容器",
			Description: "重启指定容器。",
			InputSchema: objectSchema(map[string]any{"id": stringProp("容器 ID 或名称")}, "id"),
			Annotations: mutateAnnotations(false),
		},
		{
			Name:        "list_images",
			Title:       "列出镜像",
			Description: "列出本地 Docker 镜像，含标签、大小、是否被使用。",
			InputSchema: objectSchema(nil),
			Annotations: readOnlyAnnotations(),
		},
		{
			Name:        "list_compose_files",
			Title:       "列出 compose 文件",
			Description: "列出 stacks 目录下的 compose 文件。",
			InputSchema: objectSchema(nil),
			Annotations: readOnlyAnnotations(),
		},
		{
			Name:        "read_compose_file",
			Title:       "读取 compose 文件",
			Description: "读取指定 compose 文件的完整内容。",
			InputSchema: objectSchema(map[string]any{"filename": stringProp("文件名，如 demo.yaml")}, "filename"),
			Annotations: readOnlyAnnotations(),
		},
		{
			Name:        "write_compose_file",
			Title:       "写入 compose 文件",
			Description: "保存 compose 文件内容（不存在则创建）。覆盖已有配置前请先读取确认。",
			InputSchema: objectSchema(map[string]any{
				"filename": stringProp("文件名，如 demo.yaml"),
				"content":  stringProp("完整的 compose YAML 内容"),
			}, "filename", "content"),
			Annotations: mutateAnnotations(true),
		},
		{
			Name:        "validate_compose_file",
			Title:       "校验 compose 内容",
			Description: "校验 compose YAML 结构，不落盘。建议在写入前调用。",
			InputSchema: objectSchema(map[string]any{
				"filename": stringProp("文件名"),
				"content":  stringProp("待校验的 compose YAML"),
			}, "filename", "content"),
			Annotations: readOnlyAnnotations(),
		},
		{
			Name:        "deploy_compose",
			Title:       "部署 compose",
			Description: "投递 compose 执行任务，返回 jobID。action 取值：deploy（拉取并启动）/ up（用本地镜像启动）/ down / restart / pull。执行是异步的，用 get_compose_job 查询结果。",
			InputSchema: objectSchema(map[string]any{
				"filename": stringProp("文件名"),
				"action": map[string]any{
					"type":        "string",
					"description": "执行动作",
					"enum":        []string{"deploy", "up", "down", "restart", "pull"},
					"default":     "deploy",
				},
			}, "filename"),
			Annotations: mutateAnnotations(true),
		},
		{
			Name:        "get_compose_job",
			Title:       "查询 compose 任务",
			Description: "按 jobID 查询任务状态与结果。status 为 queued/running/succeeded/failed/interrupted/not_found，done=true 才是终态；interrupted 表示结果不确定需人工核验。",
			InputSchema: objectSchema(map[string]any{"jobId": stringProp("deploy_compose 返回的 jobID")}, "jobId"),
			Annotations: readOnlyAnnotations(),
		},
		{
			Name:        "compose_history",
			Title:       "查看执行历史",
			Description: "查看 compose 执行历史（活动任务 + 近期终态）。",
			InputSchema: objectSchema(nil),
			Annotations: readOnlyAnnotations(),
		},
	}
}

// Call 执行工具并返回文本结果。
func (r *Registry) Call(ctx context.Context, name string, args map[string]any) *ToolCallResult {
	switch name {
	case "list_containers":
		resp, err := containerlogic.NewContainersListLogic(ctx, r.svcCtx).ContainersList()
		return jsonOrError(resp, err)

	case "container_logs":
		id, err := requireString(args, "id")
		if err != nil {
			return ErrorResult(err.Error())
		}
		req := &types.ContainerLogsReq{
			IdReq:      types.IdReq{Id: id},
			Tail:       intArg(args, "tail", 200),
			Timestamps: boolArg(args, "timestamps"),
		}
		logx.Infof("mcp: container_logs id=%s tail=%d", id, req.Tail)
		resp, err := containerlogic.NewLogsLogic(ctx, r.svcCtx).Logs(req)
		return jsonOrError(resp, err)

	case "start_container", "stop_container", "restart_container":
		id, err := requireString(args, "id")
		if err != nil {
			return ErrorResult(err.Error())
		}
		logx.Infof("mcp: %s id=%s", name, id)
		req := &types.IdReq{Id: id}
		var resp *types.Resp
		switch name {
		case "start_container":
			resp, err = containerlogic.NewStartLogic(ctx, r.svcCtx).Start(req)
		case "stop_container":
			resp, err = containerlogic.NewStopLogic(ctx, r.svcCtx).Stop(req)
		case "restart_container":
			resp, err = containerlogic.NewRestartLogic(ctx, r.svcCtx).Restart(req)
		}
		return jsonOrError(resp, err)

	case "list_images":
		resp, err := imagelogic.NewImagesListLogic(ctx, r.svcCtx).ImagesList()
		return jsonOrError(resp, err)

	case "list_compose_files":
		resp, err := composelogic.NewLogic(ctx, r.svcCtx).List()
		return jsonOrError(resp, err)

	case "read_compose_file":
		filename, err := requireString(args, "filename")
		if err != nil {
			return ErrorResult(err.Error())
		}
		resp, err := composelogic.NewLogic(ctx, r.svcCtx).Read(&types.ComposeReadReq{Filename: filename})
		return jsonOrError(resp, err)

	case "write_compose_file":
		filename, err := requireString(args, "filename")
		if err != nil {
			return ErrorResult(err.Error())
		}
		content, err := requireString(args, "content")
		if err != nil {
			return ErrorResult(err.Error())
		}
		logx.Infof("mcp: write_compose_file filename=%s bytes=%d", filename, len(content))
		resp, err := composelogic.NewLogic(ctx, r.svcCtx).Save(&types.ComposeFileReq{
			Filename: filename,
			Content:  content,
		})
		return jsonOrError(resp, err)

	case "validate_compose_file":
		filename, err := requireString(args, "filename")
		if err != nil {
			return ErrorResult(err.Error())
		}
		content, err := requireString(args, "content")
		if err != nil {
			return ErrorResult(err.Error())
		}
		resp, err := composelogic.NewLogic(ctx, r.svcCtx).Validate(&types.ComposeFileReq{
			Filename: filename,
			Content:  content,
		})
		return jsonOrError(resp, err)

	case "deploy_compose":
		filename, err := requireString(args, "filename")
		if err != nil {
			return ErrorResult(err.Error())
		}
		action := strings.TrimSpace(stringArg(args, "action"))
		if action == "" {
			action = "deploy"
		}
		logx.Infof("mcp: deploy_compose filename=%s action=%s", filename, action)
		resp, err := composelogic.NewLogic(ctx, r.svcCtx).Deploy(&types.ComposeDeployReq{
			Filename: filename,
			Action:   action,
		})
		return jsonOrError(resp, err)

	case "get_compose_job":
		jobID, err := requireString(args, "jobId")
		if err != nil {
			return ErrorResult(err.Error())
		}
		resp, err := composelogic.NewLogic(ctx, r.svcCtx).Result(&types.ComposeJobReq{JobID: jobID})
		return jsonOrError(resp, err)

	case "compose_history":
		resp, err := composelogic.NewLogic(ctx, r.svcCtx).History()
		return jsonOrError(resp, err)

	default:
		return ErrorResult(fmt.Sprintf("未知工具：%s", name))
	}
}

// jsonOrError 把 logic 的 (Resp, error) 统一成工具结果。
// 业务失败（code != 200）也标为 isError，让模型能明确看到失败原因。
func jsonOrError(resp *types.Resp, err error) *ToolCallResult {
	if err != nil && resp == nil {
		return ErrorResult(err.Error())
	}
	if resp == nil {
		return ErrorResult("服务未返回结果")
	}
	payload, marshalErr := json.MarshalIndent(resp, "", "  ")
	if marshalErr != nil {
		return ErrorResult("结果序列化失败")
	}
	if resp.Code != 0 && resp.Code != 200 {
		return &ToolCallResult{
			Content: []Content{{Type: "text", Text: string(payload)}},
			IsError: true,
		}
	}
	return TextResult(string(payload))
}

// MARK: - schema 辅助

func objectSchema(props map[string]any, required ...string) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	if props == nil {
		schema["properties"] = map[string]any{}
	}
	return schema
}

func stringProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func requireString(args map[string]any, key string) (string, error) {
	value := strings.TrimSpace(stringArg(args, key))
	if value == "" {
		return "", fmt.Errorf("缺少参数 %s", key)
	}
	return value, nil
}

func stringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if value, ok := args[key].(string); ok {
		return value
	}
	return ""
}

func intArg(args map[string]any, key string, fallback int) int {
	if args == nil {
		return fallback
	}
	switch value := args[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case json.Number:
		if n, err := value.Int64(); err == nil {
			return int(n)
		}
	}
	return fallback
}

func boolArg(args map[string]any, key string) bool {
	if args == nil {
		return false
	}
	value, _ := args[key].(bool)
	return value
}
