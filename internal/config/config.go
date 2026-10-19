package config

import "github.com/zeromicro/go-zero/rest"

type Config struct {
	rest.RestConf
	Auth struct { // JWT 认证需要的密钥和过期时间配置
		AccessSecret string
		AccessExpire int64
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

// ComposeDir 返回 compose 文件目录。
func (c Config) ComposeDir() string {
	if c.Compose.Dir == "" {
		return "/data/stacks"
	}
	return c.Compose.Dir
}
