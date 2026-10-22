package config

import (
	"os"
	"time"

	"github.com/zeromicro/go-zero/rest"
)

const (
	// EnvReleaseWatchInterval 关注仓库发布检查间隔（分钟）的环境变量名
	EnvReleaseWatchInterval = "RELEASE_WATCH_INTERVAL_MINUTES"
	// DefaultReleaseWatchInterval 检查间隔缺省值
	DefaultReleaseWatchInterval = "60"
	// EnvComposeBuiltinExecutor 是否启用内置 compose 执行器的环境变量名
	EnvComposeBuiltinExecutor = "COMPOSE_BUILTIN_EXECUTOR"
	// DefaultComposeBuiltinExecutor 内置执行器缺省值，默认启用
	DefaultComposeBuiltinExecutor = "true"
)

// ApplyEnvDefaults 给 yaml 模板引用了但用户未设置的环境变量补默认值。
// go-zero 的 UseEnv 把缺失变量展开成空字符串，int 字段会 type mismatch 导致启动失败。
func ApplyEnvDefaults() {
	if os.Getenv(EnvReleaseWatchInterval) == "" {
		os.Setenv(EnvReleaseWatchInterval, DefaultReleaseWatchInterval)
	}
	if os.Getenv(EnvComposeBuiltinExecutor) == "" {
		os.Setenv(EnvComposeBuiltinExecutor, DefaultComposeBuiltinExecutor)
	}
}

type Config struct {
	rest.RestConf
	Auth struct { // JWT 认证需要的密钥和过期时间配置
		// AccessSecret optional：环境变量未设置时 yaml 展开为空值，由 main 里的 SecretKeyValid 校验兜底
		AccessSecret string `json:",optional"`
		AccessExpire int64
	}
	ReleaseWatch struct { // 关注仓库发布监控
		// IntervalMinutes 检查间隔，缺失时由 ApplyEnvDefaults 补默认值（yaml ${VAR} 展开空值会让 int 字段加载失败）
		IntervalMinutes int `json:",optional"`
		// GitHubToken 可选，用于提高 GitHub API 频率上限
		GitHubToken string `json:",optional"`
		// DataDir 关注列表与去重状态的存放目录，为空时用 /data/config
		DataDir string `json:",optional"`
	}
	Compose struct { // compose 文件管理
		// Dir compose 文件存放目录，为空时用 /data/stacks
		Dir string `json:",optional"`
		// BuiltinExecutor 是否启用内置 compose 执行器，默认 true（由 ApplyEnvDefaults 补齐），
		// 置 false 时仅依赖宿主机 watcher
		BuiltinExecutor bool `json:",optional"`
	}
}

var (
	Version   string
	BuildDate string
)

// CheckInterval 返回检查间隔，未设置按 60 分钟，最小 5 分钟，避免对 GitHub 造成过高频率。
func (c Config) CheckInterval() time.Duration {
	minutes := c.ReleaseWatch.IntervalMinutes
	if minutes <= 0 {
		minutes = 60
	}
	if minutes < 5 {
		minutes = 5
	}
	return time.Duration(minutes) * time.Minute
}

// SecretKeyValid 校验登录密码：非纯数字且不少于 8 位。
func (c Config) SecretKeyValid() bool {
	secret := c.Auth.AccessSecret
	if len(secret) < 8 {
		return false
	}
	for _, r := range secret {
		if r < '0' || r > '9' {
			return true
		}
	}
	return false
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
