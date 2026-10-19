package config

import (
	"time"

	"github.com/zeromicro/go-zero/rest"
)

type Config struct {
	rest.RestConf
	Auth struct { // JWT 认证需要的密钥和过期时间配置
		AccessSecret string
		AccessExpire int64
	}
	ReleaseWatch struct { // 关注仓库发布监控
		// IntervalMinutes 检查间隔，小于 5 分钟按 5 分钟处理
		IntervalMinutes int `json:",default=60"`
		// GitHubToken 可选，用于提高 GitHub API 频率上限
		GitHubToken string `json:",optional"`
		// DataDir 关注列表与去重状态的存放目录，为空时用 /data/config
		DataDir string `json:",optional"`
	}
	Compose struct { // compose 文件管理
		// Dir compose 文件存放目录，为空时用 /data/stacks
		Dir string `json:",optional"`
	}
}

var (
	Version   string
	BuildDate string
)

// CheckInterval 返回检查间隔，最小 5 分钟，避免对 GitHub 造成过高频率。
func (c Config) CheckInterval() time.Duration {
	minutes := c.ReleaseWatch.IntervalMinutes
	if minutes < 5 {
		minutes = 5
	}
	return time.Duration(minutes) * time.Minute
}

// ReleaseWatchDataDir 返回关注列表的存放目录。
func (c Config) ReleaseWatchDataDir() string {
	if c.ReleaseWatch.DataDir == "" {
		return "/data/config"
	}
	return c.ReleaseWatch.DataDir
}

// ComposeDir 返回 compose 文件目录。
func (c Config) ComposeDir() string {
	if c.Compose.Dir == "" {
		return "/data/stacks"
	}
	return c.Compose.Dir
}
