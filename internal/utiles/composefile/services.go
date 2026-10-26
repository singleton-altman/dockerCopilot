package composefile

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

// 部署后的服务状态快照：up/deploy/restart 成功后，按 compose 文件精确列出
// 本文件创建的容器并逐个 inspect，产出 JobResult.services 与 stage=ps 的结论步骤。
//
// 为什么不解析 compose up 的输出：容器是否真的健康只有 daemon 说了算，
// 文本归纳只能反映"命令没报错"。同样，这里只做只读查询，不等待、不门禁：
// 健康结论只来自容器 Health 状态，不宣称应用层可用性。

// ServiceState 单个服务的运行快照。Health 仅容器配置了 healthcheck 时非空，
// 取值为 healthy / unhealthy / starting（即容器 State.Health.Status 原值）。
type ServiceState struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Health string `json:"health,omitempty"`
}

// containerSource 服务采集用到的 docker 只读能力，*client.Client 天然满足。
// 抽成接口只是为了测试时注入假实现，不改变线上路径。
type containerSource interface {
	ContainerList(ctx context.Context, options container.ListOptions) ([]types.Container, error)
	ContainerInspect(ctx context.Context, containerID string) (types.ContainerJSON, error)
}

// serviceSource 返回采集用的 docker 只读客户端；未注入时返回 nil，调用方降级为 warning。
func (e *Executor) serviceSource() containerSource {
	if e.dockerAPI != nil {
		return e.dockerAPI
	}
	if e.docker != nil {
		return e.docker
	}
	return nil
}

// serviceSnapshotApplies 判断该任务是否需要（且允许）采集服务状态：
// 仅 up/deploy/restart 且命令层已成功、未被中断的任务；down/pull 不产出。
func (e *Executor) serviceSnapshotApplies(job Job, result JobResult) bool {
	if !result.Success || result.Status == StatusInterrupted {
		return false
	}
	switch job.Action {
	case ActionUp, ActionDeploy, ActionRestart:
		return true
	}
	return false
}

// attachServiceSnapshot 采集服务状态并追加 stage=ps 的步骤。
// 只读且失败降级：枚举失败或未注入 docker client 都只记 warning，不推翻已成功的 up 结论。
// 仅对成功且未中断的 up/deploy/restart 生效；down/pull 与失败任务原样返回。
func (e *Executor) attachServiceSnapshot(ctx context.Context, job Job, result JobResult) JobResult {
	if !e.serviceSnapshotApplies(job, result) {
		return result
	}
	source := e.serviceSource()
	if source == nil {
		step, _ := summarizeServiceStates(nil, "未注入 Docker 客户端，服务端无法采集服务运行状态")
		result.Steps = append(result.Steps, step)
		return result
	}
	services, warning := e.collectOwnServices(ctx, source, job.Filename)
	step, collected := summarizeServiceStates(services, warning)
	if collected != nil {
		result.Services = collected
	}
	result.Steps = append(result.Steps, step)
	return result
}

// collectOwnServices 按 com.docker.compose.project.config_files 标签精确筛选
// 本文件的容器（过滤在 daemon 侧完成，不是全量 list），再逐个 inspect 取健康状态。
// 返回 services 与 warning（空串表示枚举成功）。
func (e *Executor) collectOwnServices(ctx context.Context, source containerSource, filename string) ([]ServiceState, string) {
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	target := e.composeFilePath(filename)
	items, err := source.ContainerList(listCtx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", labelConfigFiles+"="+target),
		),
	})
	if err != nil {
		return nil, fmt.Sprintf("查询本文件容器失败: %v", err)
	}

	sort.Slice(items, func(i, j int) bool {
		return serviceNameOf(items[i]) < serviceNameOf(items[j])
	})
	services := make([]ServiceState, 0, len(items))
	for _, item := range items {
		state := ServiceState{Name: serviceNameOf(item), State: item.State}
		// 只 inspect 本文件的容器；单个 inspect 失败退回 list 的状态，不记健康结论。
		inspectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		detail, err := source.ContainerInspect(inspectCtx, item.ID)
		cancel()
		if err == nil && detail.State != nil {
			state.State = detail.State.Status
			if detail.State.Health != nil && detail.State.Health.Status != "" {
				state.Health = detail.State.Health.Status
			}
		}
		services = append(services, state)
	}
	return services, ""
}

// serviceNameOf 优先取 compose 的 service 标签，缺失时回退容器名（去掉首斜杠）与 ID 前缀。
func serviceNameOf(item types.Container) string {
	if name := item.Labels[labelService]; name != "" {
		return name
	}
	if len(item.Names) > 0 {
		return strings.TrimPrefix(item.Names[0], "/")
	}
	return item.ID
}

// summarizeServiceStates 把服务快照归纳为 stage=ps 的结论：
// 全部 running 视为 success；存在非 running（如 exited）或 health=unhealthy 记 warning；
// warning 非空表示枚举本身失败（含未注入 client），同样只记 warning。
func summarizeServiceStates(services []ServiceState, warning string) (JobStep, []ServiceState) {
	step := JobStep{Stage: "ps"}
	if warning != "" {
		step.Status = "warning"
		step.Message = warning
		return step, nil
	}
	var bad []string
	healthy := 0
	for _, svc := range services {
		switch {
		case svc.Health == "unhealthy":
			bad = append(bad, fmt.Sprintf("%s（unhealthy）", svc.Name))
		case svc.State != "running":
			bad = append(bad, fmt.Sprintf("%s（%s）", svc.Name, svc.State))
		}
		if svc.Health == "healthy" {
			healthy++
		}
	}
	if len(bad) > 0 {
		step.Status = "warning"
		step.Message = "有服务未处于运行中或健康检查失败：" + strings.Join(bad, "、")
		return step, services
	}
	step.Status = "success"
	switch {
	case len(services) == 0:
		step.Message = "没有发现本文件创建的容器"
	case healthy > 0:
		step.Message = fmt.Sprintf("%d 个服务全部运行中，其中 %d 个健康检查通过", len(services), healthy)
	default:
		step.Message = fmt.Sprintf("%d 个服务全部运行中", len(services))
	}
	return step, services
}
