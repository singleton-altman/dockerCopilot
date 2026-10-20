package utiles

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	dockerTypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/onlyLTY/dockerCopilot/internal/module"
	"github.com/onlyLTY/dockerCopilot/internal/svc"
	"github.com/zeromicro/go-zero/core/logx"
)

const statsCollectTimeout = 8 * time.Second

// ContainerStats 是一个容器的运行时资源快照。
// cpuPercent / netRxRate / netTxRate 由相邻两次采样做差得到，
// 服务端刚启动或容器刚启动后的首次请求为 0，下一次轮询起为真实值。
type ContainerStats struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	State         string  `json:"state"`
	CPUPercent    float64 `json:"cpuPercent"`
	MemoryUsage   uint64  `json:"memoryUsage"`
	MemoryLimit   uint64  `json:"memoryLimit"`
	MemoryPercent float64 `json:"memoryPercent"`
	NetRx         uint64  `json:"netRx"`
	NetTx         uint64  `json:"netTx"`
	NetRxRate     float64 `json:"netRxRate"`
	NetTxRate     float64 `json:"netTxRate"`
	BlockRead     uint64  `json:"blockRead"`
	BlockWrite    uint64  `json:"blockWrite"`
	Pids          uint64  `json:"pids"`
}

// GetContainerStats 并行采集所有 running 容器的 one-shot stats 并计算指标。
// 单个容器采样失败只跳过该容器，不影响整体。
func GetContainerStats(ctx *svc.ServiceContext) ([]ContainerStats, error) {
	cli := ctx.DockerClient
	if cli == nil {
		return nil, errors.New("docker client unavailable")
	}
	listCtx, cancel := context.WithTimeout(context.Background(), statsCollectTimeout)
	defer cancel()
	running, err := cli.ContainerList(listCtx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("status", "running")),
	})
	if err != nil {
		logx.Errorf("get container list for stats error: %v", err)
		return nil, err
	}

	statsCtx, cancel := context.WithTimeout(context.Background(), statsCollectTimeout)
	defer cancel()
	results := make([]ContainerStats, len(running))
	errs := make([]error, len(running))
	var wg sync.WaitGroup
	for i, dockerContainerInfo := range running {
		wg.Add(1)
		go func(i int, info dockerTypes.Container) {
			defer wg.Done()
			sample, err := collectSample(statsCtx, cli, info.ID)
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = buildStats(ctx, info, sample)
		}(i, dockerContainerInfo)
	}
	wg.Wait()

	seen := make(map[string]bool, len(running))
	var statsList []ContainerStats
	for i, info := range running {
		if errs[i] != nil {
			logx.Errorf("get stats for container %s error: %v", info.ID[:12], errs[i])
			continue
		}
		seen[info.ID] = true
		statsList = append(statsList, results[i])
	}
	ctx.ContainerStatsCache.Prune(seen)
	return statsList, nil
}

func collectSample(ctx context.Context, cli *client.Client, id string) (container.StatsResponse, error) {
	reader, err := cli.ContainerStatsOneShot(ctx, id)
	if err != nil {
		return container.StatsResponse{}, err
	}
	defer reader.Body.Close()
	body, err := io.ReadAll(reader.Body)
	if err != nil {
		return container.StatsResponse{}, err
	}
	var stats container.StatsResponse
	err = json.Unmarshal(body, &stats)
	return stats, err
}

// buildStats 结合缓存中的上一次采样计算 CPU 占用与网络速率，并保存本次采样。
func buildStats(ctx *svc.ServiceContext, info dockerTypes.Container, stats container.StatsResponse) ContainerStats {
	name := ""
	if len(info.Names) > 0 && len(info.Names[0]) > 1 {
		name = info.Names[0][1:]
	}
	result := ContainerStats{
		ID:    info.ID,
		Name:  name,
		State: info.State,
	}

	result.MemoryUsage, result.MemoryLimit, result.MemoryPercent = memoryUsage(stats.MemoryStats)
	result.NetRx, result.NetTx = networkTotals(stats)
	result.BlockRead, result.BlockWrite = blockTotals(stats)
	result.Pids = stats.PidsStats.Current

	sample := module.ContainerStatsSample{
		CPUTotal:    stats.CPUStats.CPUUsage.TotalUsage,
		SystemTotal: stats.CPUStats.SystemUsage,
		NetRx:       result.NetRx,
		NetTx:       result.NetTx,
		Time:        time.Now(),
	}
	prev, ok := ctx.ContainerStatsCache.Previous(info.ID)
	if ok {
		cpuDelta := int64(sample.CPUTotal - prev.CPUTotal)
		systemDelta := int64(sample.SystemTotal - prev.SystemTotal)
		if systemDelta > 0 && cpuDelta > 0 && cpuDelta <= systemDelta {
			onlineCPUs := stats.CPUStats.OnlineCPUs
			if onlineCPUs == 0 {
				onlineCPUs = uint32(len(stats.CPUStats.CPUUsage.PercpuUsage))
				if onlineCPUs == 0 {
					onlineCPUs = 1
				}
			}
			result.CPUPercent = float64(cpuDelta) / float64(systemDelta) * float64(onlineCPUs) * 100
		}
		elapsed := sample.Time.Sub(prev.Time).Seconds()
		if elapsed > 0 {
			result.NetRxRate = float64(int64(sample.NetRx-prev.NetRx)) / elapsed
			result.NetTxRate = float64(int64(sample.NetTx-prev.NetTx)) / elapsed
		}
	}
	ctx.ContainerStatsCache.Store(info.ID, sample)
	return result
}

// memoryUsage 与 docker stats 口径一致：扣除 inactive_file 页缓存。
func memoryUsage(mem container.MemoryStats) (uint64, uint64, float64) {
	usage := mem.Usage
	if v, ok := mem.Stats["inactive_file"]; ok {
		if v < usage {
			usage -= v
		}
	} else if v, ok := mem.Stats["total_inactive_file"]; ok && v < usage {
		usage -= v
	}
	if mem.Limit == 0 {
		return usage, mem.Limit, 0
	}
	return usage, mem.Limit, float64(usage) / float64(mem.Limit) * 100
}

func networkTotals(stats container.StatsResponse) (uint64, uint64) {
	var rx, tx uint64
	for _, net := range stats.Networks {
		rx += net.RxBytes
		tx += net.TxBytes
	}
	return rx, tx
}

func blockTotals(stats container.StatsResponse) (uint64, uint64) {
	var read, write uint64
	for _, entry := range stats.BlkioStats.IoServiceBytesRecursive {
		switch entry.Op {
		case "Read", "read":
			read += entry.Value
		case "Write", "write":
			write += entry.Value
		}
	}
	return read, write
}
