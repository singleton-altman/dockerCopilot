package main

import (
	"testing"
	"time"

	"github.com/onlyLTY/dockerCopilot/internal/config"
	"github.com/zeromicro/go-zero/core/conf"
)

// 只设置 secretKey、其余环境变量全部缺失时，配置必须仍能加载（线上踩过：
// yaml 里 ${RELEASE_WATCH_INTERVAL_MINUTES} 展开成空字符串，int 字段直接 type mismatch）。
// 因此 main 启动时先跑 ApplyEnvDefaults，这里按同样顺序验证。
func TestLoadConfigWithMissingOptionalEnv(t *testing.T) {
	t.Setenv("secretKey", "4sP!aX7zU")
	t.Setenv(config.EnvReleaseWatchInterval, "") // 模拟完全未设置

	config.ApplyEnvDefaults()

	var c config.Config
	if err := conf.Load("etc/dockerCopilot.yaml", &c, conf.UseEnv()); err != nil {
		t.Fatalf("仅设置 secretKey 时加载失败: %v", err)
	}
	if got := c.CheckInterval(); got != 60*time.Minute {
		t.Fatalf("未设置间隔时默认应为 60 分钟，实际 %v", got)
	}
	if !c.SecretKeyValid() {
		t.Fatal("secretKey 校验应通过")
	}
}

func TestLoadConfigWithIntervalEnv(t *testing.T) {
	t.Setenv("secretKey", "4sP!aX7zU")
	t.Setenv(config.EnvReleaseWatchInterval, "10")

	var c config.Config
	if err := conf.Load("etc/dockerCopilot.yaml", &c, conf.UseEnv()); err != nil {
		t.Fatalf("设置间隔时加载失败: %v", err)
	}
	if got := c.CheckInterval(); got != 10*time.Minute {
		t.Fatalf("间隔应为 10 分钟，实际 %v", got)
	}
}

func TestCheckInterval(t *testing.T) {
	cases := []struct {
		minutes int
		want    time.Duration
	}{
		{0, 60 * time.Minute},
		{60, 60 * time.Minute},
		{10, 10 * time.Minute},
		{3, 5 * time.Minute},
	}
	for _, tc := range cases {
		c := config.Config{}
		c.ReleaseWatch.IntervalMinutes = tc.minutes
		if got := c.CheckInterval(); got != tc.want {
			t.Fatalf("IntervalMinutes=%d 期望 %v，实际 %v", tc.minutes, tc.want, got)
		}
	}
}

func TestSecretKeyValid(t *testing.T) {
	cases := []struct {
		secret string
		want   bool
	}{
		{"4sP!aX7zU", true},
		{"abcdefgh", true},
		{"123456789", false},
		{"12345678", false},
		{"short1", false},
		{"", false},
	}
	for _, tc := range cases {
		c := config.Config{}
		c.Auth.AccessSecret = tc.secret
		if got := c.SecretKeyValid(); got != tc.want {
			t.Fatalf("secretKey=%q 期望 %v，实际 %v", tc.secret, tc.want, got)
		}
	}
}
