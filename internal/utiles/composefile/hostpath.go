package composefile

import (
	"bytes"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// MountView 抽象容器挂载信息，便于纯函数测试。
type MountView struct {
	Type        string // bind / volume
	Source      string // bind: 宿主机路径；volume: 卷名
	Destination string // 容器内路径
}

// HostPathForDir 在容器挂载表中查找 dir 的宿主机真实路径。
// 取目的路径是 dir 或其祖先的最长 bind 挂载，按相对部分拼接：
// dir=/data/stacks 且存在 bind /vol1/docker/dc/data:/data 时返回 /vol1/docker/dc/data/stacks。
// 找不到返回空串。
func HostPathForDir(mounts []MountView, dir string) string {
	if dir == "" || !filepath.IsAbs(dir) {
		return ""
	}
	bestDest, bestSrc := "", ""
	for _, m := range mounts {
		if m.Type != "bind" || m.Source == "" || !filepath.IsAbs(m.Destination) {
			continue
		}
		if m.Destination != dir && !strings.HasPrefix(dir, m.Destination+"/") {
			continue
		}
		if len(m.Destination) >= len(bestDest) {
			bestDest, bestSrc = m.Destination, m.Source
		}
	}
	if bestDest == "" {
		return ""
	}
	rel, err := filepath.Rel(bestDest, dir)
	if err != nil {
		return ""
	}
	return filepath.Join(bestSrc, rel)
}

// RewriteRelativeVolumes 把 compose YAML 里相对路径的 bind 挂载源改写为 hostDir 下的绝对路径。
// 服务端在容器内执行 compose，而挂载由宿主机 daemon 解释：相对路径会按 project-directory
// （容器内路径）解析，daemon 会在宿主机错误位置创建空目录。改写后 daemon 直接用宿主机路径。
// 只处理 services.*.volumes；命名卷、type: volume、绝对路径保持原样，
// 其余内容（含注释、env_file、build 等）原样保留。
// 返回改写后的 YAML 与是否发生了改写。
func RewriteRelativeVolumes(doc string, hostDir string) (string, bool, error) {
	if hostDir == "" {
		return doc, false, nil
	}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(doc), &root); err != nil {
		return doc, false, err
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return doc, false, nil
	}
	top := root.Content[0]
	if top.Kind != yaml.MappingNode {
		return doc, false, nil
	}
	services := mappingValue(top, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return doc, false, nil
	}

	changed := false
	for _, svc := range services.Content {
		if svc.Kind != yaml.MappingNode {
			continue
		}
		vols := mappingValue(svc, "volumes")
		if vols == nil || vols.Kind != yaml.SequenceNode {
			continue
		}
		for _, item := range vols.Content {
			switch item.Kind {
			case yaml.ScalarNode:
				if rewritten, ok := rewriteShortVolume(item.Value, hostDir); ok {
					item.Value = rewritten
					changed = true
				}
			case yaml.MappingNode:
				if t := mappingValue(item, "type"); t != nil && t.Value == "volume" {
					continue
				}
				src := mappingValue(item, "source")
				if src != nil && src.Kind == yaml.ScalarNode {
					if rewritten, ok := rewritePathSource(src.Value, hostDir); ok {
						src.Value = rewritten
						changed = true
					}
				}
			}
		}
	}
	if !changed {
		return doc, false, nil
	}

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return doc, false, err
	}
	if err := enc.Close(); err != nil {
		return doc, false, err
	}
	return out.String(), true, nil
}

func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// rewriteShortVolume 处理 "src:dst[:mode]" 短语法，只改写相对路径源。
func rewriteShortVolume(v, hostDir string) (string, bool) {
	parts := strings.Split(v, ":")
	if len(parts) < 2 {
		// 无冒号的命名卷/匿名卷不动
		return "", false
	}
	newSrc, ok := rewritePathSource(parts[0], hostDir)
	if !ok {
		return "", false
	}
	parts[0] = newSrc
	return strings.Join(parts, ":"), true
}

// rewritePathSource 判断单个路径源是否为需要改写的相对路径。
// ./x、../x、. 视为相对路径；绝对路径、~ 与命名卷保持原样。
func rewritePathSource(src, hostDir string) (string, bool) {
	if src == "" || src == "~" || strings.HasPrefix(src, "~/") {
		return "", false
	}
	switch {
	case src == ".":
		return hostDir, true
	case strings.HasPrefix(src, "./"):
		return filepath.Join(hostDir, src[2:]), true
	case strings.HasPrefix(src, "../"):
		return filepath.Join(hostDir, src), true
	}
	return "", false
}
