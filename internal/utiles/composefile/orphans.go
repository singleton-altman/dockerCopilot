package composefile

import (
	"context"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/zeromicro/go-zero/core/logx"
)

// 用于识别容器归属的 compose 标签
const (
	labelService     = "com.docker.compose.service"
	labelConfigFiles = "com.docker.compose.project.config_files"
)

// cleanOwnOrphans 清理「属于当前 compose 文件、但服务已被删除」的遗留容器。
//
// 为什么不用 --remove-orphans：同一 COMPOSE_DIR 下的多个 compose 文件默认
// 共用目录名作为 project，该参数会把别的文件创建的容器也当成孤儿删掉。
// 这里只处理同时满足以下条件的容器：
//  1. config_files 标签指向当前文件（说明是它创建的）
//  2. 其 service 已不在当前文件的服务列表里
//
// 依赖不可用时直接跳过：宁可留下孤儿容器，也不能误删别人的容器。
func (e *Executor) cleanOwnOrphans(ctx context.Context, job Job) {
	if e.docker == nil {
		return
	}

	present, err := e.declaredServices(job.Filename)
	if err != nil {
		logx.Errorf("读取 compose 服务列表失败，跳过孤儿清理: %v", err)
		return
	}

	removed := e.removeOrphanContainers(ctx, job.Filename, present)
	if removed > 0 {
		logx.Infof("已清理 %d 个本文件遗留的孤儿容器 (%s)", removed, job.Filename)
	}
}

// removeOrphanContainers 删除属于 filename 但服务已不在 present 中的容器。
func (e *Executor) removeOrphanContainers(ctx context.Context, filename string, present map[string]bool) int {
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	target := e.composeFilePath(filename)
	containers, err := e.docker.ContainerList(listCtx, container.ListOptions{All: true})
	if err != nil {
		return 0
	}

	removed := 0
	for _, item := range containers {
		if item.Labels[labelConfigFiles] != target {
			continue
		}
		service := item.Labels[labelService]
		// 服务仍在当前文件里，或者标签缺失，都不动
		if service == "" || present[service] {
			continue
		}
		rmCtx, rmCancel := context.WithTimeout(ctx, 60*time.Second)
		err := e.docker.ContainerRemove(rmCtx, item.ID, container.RemoveOptions{Force: true})
		rmCancel()
		if err == nil {
			removed++
		}
	}
	return removed
}

// declaredServices 返回当前 compose 文件声明的服务名集合。
// 直接解析文件，不调用 docker，避免额外进程与超时。
func (e *Executor) declaredServices(filename string) (map[string]bool, error) {
	services, err := parseServiceNames(e.composeFilePath(filename))
	if err != nil {
		return nil, err
	}
	present := make(map[string]bool, len(services))
	for _, name := range services {
		present[name] = true
	}
	return present, nil
}

// composeFilePath 返回 compose 文件绝对路径。
func (e *Executor) composeFilePath(filename string) string {
	return strings.TrimRight(e.workDir, "/") + "/" + filename
}
