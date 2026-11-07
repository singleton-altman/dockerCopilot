package utiles

import (
	"context"
	"os"
	"time"

	"github.com/onlyLTY/dockerCopilot/internal/svc"
	"github.com/onlyLTY/dockerCopilot/internal/utiles/composefile"
)

// DetectHostComposeDir 通过 docker.sock 自检本容器，推导 compose 目录在宿主机上的真实路径。
// 容器默认 hostname 是短容器 ID，可据此 inspect 自身挂载：找到覆盖 compose 目录的
// 最长 bind 挂载，把容器内前缀替换为宿主机源路径。非容器环境、自定义 hostname、
// docker API 不可用等情形返回空串，执行器保持原行为。
// 带 10 秒超时：本函数在 main 启动路径上同步执行，daemon 异常时宁可放弃探测也不能卡住启动。
func DetectHostComposeDir(ctx *svc.ServiceContext) string {
	if ctx == nil || ctx.DockerClient == nil {
		return ""
	}
	hostname, err := os.Hostname()
	if err != nil {
		return ""
	}
	detectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	inspected, err := ctx.DockerClient.ContainerInspect(detectCtx, hostname)
	if err != nil {
		return ""
	}
	mounts := make([]composefile.MountView, 0, len(inspected.Mounts))
	for _, m := range inspected.Mounts {
		mounts = append(mounts, composefile.MountView{
			Type:        string(m.Type),
			Source:      m.Source,
			Destination: m.Destination,
		})
	}
	return composefile.HostPathForDir(mounts, ctx.Config.ComposeDir())
}
