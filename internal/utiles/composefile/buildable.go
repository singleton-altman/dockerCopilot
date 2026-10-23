package composefile

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// buildableImages 读取 compose 文件，返回声明了 build 的服务所用的镜像名。
//
// 这类镜像 registry 上本来就不存在，pull 必然失败；把它和「真的拉不下来」
// 区分开，才不会在过程日志里误报警告。
//
// 解析失败时返回空集合：调用方退化为按普通镜像处理，不影响主流程。
func buildableImages(workDir, filename string) map[string]bool {
	result := map[string]bool{}

	path := filepath.Join(workDir, filename)
	content, err := os.ReadFile(path)
	if err != nil {
		return result
	}

	var doc struct {
		Services map[string]struct {
			Image string `yaml:"image"`
			Build any    `yaml:"build"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return result
	}

	for _, svc := range doc.Services {
		// build 可以是字符串或映射，只要非空就视为需要构建
		if svc.Build == nil {
			continue
		}
		if image := svc.Image; image != "" {
			result[image] = true
		}
	}
	return result
}
