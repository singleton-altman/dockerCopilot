package module

import (
	"sync"
	"time"
)

// ContainerStatsSample 是一次 one-shot stats 采样的关键累计值。
// one-shot 模式下 Docker 不返回上一周期数据，CPU 占用与网络速率
// 必须由服务端拿前后两次采样做差得到。
type ContainerStatsSample struct {
	CPUTotal    uint64
	SystemTotal uint64
	NetRx       uint64
	NetTx       uint64
	Time        time.Time
}

// ContainerStatsCache 保存每个容器上一次 stats 采样，供相邻两次请求做差。
// 缓存条目超过过期时间未更新则清理，避免停止/删除的容器无限累积。
type ContainerStatsCache struct {
	mu      sync.Mutex
	samples map[string]ContainerStatsSample
	expiry  time.Duration
}

func NewContainerStatsCache() *ContainerStatsCache {
	return &ContainerStatsCache{
		samples: make(map[string]ContainerStatsSample),
		expiry:  2 * time.Minute,
	}
}

// Previous 返回该容器上一次采样；不存在时 ok=false（表现为首次轮询 CPU/速率为 0）。
func (c *ContainerStatsCache) Previous(id string) (ContainerStatsSample, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.samples[id]
	return s, ok
}

func (c *ContainerStatsCache) Store(id string, s ContainerStatsSample) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.samples[id] = s
}

// Prune 清理 seen 之外且已过期的条目。
// 传入本次请求看到的容器 id 集合，缓存中不存在于该集合且超期的即失效。
func (c *ContainerStatsCache) Prune(seen map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for id, s := range c.samples {
		if !seen[id] && now.Sub(s.Time) > c.expiry {
			delete(c.samples, id)
		}
	}
}
