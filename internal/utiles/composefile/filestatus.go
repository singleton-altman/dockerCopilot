package composefile

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

// ServiceBrief compose 文件列表里展示的单个服务状态（只读快照）。
type ServiceBrief struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Health string `json:"health,omitempty"`
}

// FileStatus 一个 compose 文件对应的运行状态汇总。
// 无容器时 Services 为空、Running=false；Docker 不可用时整个文件不返回状态。
type FileStatus struct {
	Services []ServiceBrief `json:"services"`
	Running  bool           `json:"running"`
	Healthy  bool           `json:"healthy"`
}

// CollectFileStatuses 一次 Docker 查询拿到所有 compose 文件创建的容器，
// 按 com.docker.compose.project.config_files 标签分组，供文件列表附加状态。
// 仅查询带该标签的容器（daemon 侧过滤），不枚举全部容器。
// 返回 map[文件名]状态；Docker 不可用时返回 nil，调用方省略状态字段。
func (e *Executor) CollectFileStatuses(ctx context.Context) map[string]FileStatus {
	source := e.serviceSource()
	if source == nil {
		return nil
	}
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	items, err := source.ContainerList(listCtx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", labelConfigFiles),
		),
	})
	if err != nil {
		return nil
	}

	// config_files 标签可能是逗号分隔的多个路径；按 base 名归类，与 Store.List 的文件名对应
	grouped := map[string][]ServiceBrief{}
	for _, item := range items {
		files := item.Labels[labelConfigFiles]
		if files == "" {
			continue
		}
		state := ServiceBrief{Name: serviceNameOf(item), State: item.State}
		// list 的 State 已足够（running/exited/...）；health 需 inspect，但列表查询
		// 场景不做逐个 inspect（避免 N 次调用），health 留空由前端不显示健康徽标。
		for _, path := range strings.Split(files, ",") {
			name := filepathBase(strings.TrimSpace(path))
			if name == "" {
				continue
			}
			grouped[name] = append(grouped[name], state)
		}
	}

	result := make(map[string]FileStatus, len(grouped))
	for name, services := range grouped {
		sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })
		status := FileStatus{Services: services}
		for _, svc := range services {
			if svc.State == "running" {
				status.Running = true
			}
		}
		status.Healthy = status.Running && allRunning(services)
		result[name] = status
	}
	return result
}

func allRunning(services []ServiceBrief) bool {
	for _, svc := range services {
		if svc.State != "running" {
			return false
		}
	}
	return true
}

// filepathBase 与 filepath.Base 行为一致但不引入 path 包（标签里是 Linux 路径）。
func filepathBase(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		p = p[i+1:]
	}
	if i := strings.LastIndexByte(p, '\\'); i >= 0 {
		p = p[i+1:]
	}
	return p
}
