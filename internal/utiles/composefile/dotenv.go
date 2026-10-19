package composefile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// dotenvFileName stacks 目录下的默认环境变量文件。
// 与 docker compose 的查找规则保持一致。
const dotenvFileName = ".env"

// isDotenvName 判断是否是允许管理的 dotenv 文件名。
// 支持 .env 以及 .env.production 这类变体。
func isDotenvName(name string) bool {
	return name == dotenvFileName || strings.HasPrefix(name, dotenvFileName+".")
}

// dotenvPath 返回 stacks 目录下某个 dotenv 文件的完整路径。
func (s *Store) dotenvPath(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !isDotenvName(name) {
		return "", fmt.Errorf("不是 dotenv 文件: %s", name)
	}
	if name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("非法文件名")
	}
	if !fileNamePattern.MatchString(name) {
		return "", fmt.Errorf("非法文件名")
	}

	base, err := filepath.Abs(s.dir)
	if err != nil {
		return "", err
	}
	full := filepath.Join(base, name)
	if filepath.Dir(full) != base {
		return "", fmt.Errorf("非法文件名")
	}
	return full, nil
}

// ParseDotenv 解析 dotenv 文本。
// 支持 `KEY=VALUE`、`export KEY=VALUE`、`#` 注释、单双引号包裹的值，
// 以及行尾注释（仅对未加引号的值生效）。解析失败的行会被跳过而不是中断，
// 避免一个笔误导致整份变量不可用。
func ParseDotenv(content string) map[string]string {
	values := make(map[string]string)
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// 去掉可选的 export 前缀
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(line[len("export "):])
		}
		eq := strings.Index(line, "=")
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		if !isValidEnvKey(key) {
			continue
		}
		value := strings.TrimSpace(line[eq+1:])
		values[key] = unquoteEnvValue(value)
	}
	return values
}

// isValidEnvKey 校验变量名：字母或下划线开头，后接字母数字下划线。
func isValidEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		valid := r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
			(i > 0 && r >= '0' && r <= '9')
		if !valid {
			return false
		}
	}
	return true
}

// unquoteEnvValue 去掉成对的引号；未加引号时去掉行尾注释。
func unquoteEnvValue(value string) string {
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'') {
			return value[1 : len(value)-1]
		}
	}
	// 未加引号：`#` 之后视为注释
	if idx := strings.Index(value, " #"); idx >= 0 {
		return strings.TrimSpace(value[:idx])
	}
	if strings.HasPrefix(value, "#") {
		return ""
	}
	return value
}

// loadDotenv 读取 stacks 目录下的 .env，用于校验时注入变量。
// 文件不存在时返回空 map，不视为错误。
func (s *Store) loadDotenv() map[string]string {
	path := filepath.Join(s.dir, dotenvFileName)
	content, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}
	}
	return ParseDotenv(string(content))
}

// Environment 返回用于 compose 校验的环境变量。
// 与宿主机的行为差异：这里只读容器内 stacks/.env，宿主机执行时
// docker compose 会读取自己的 .env 和进程环境变量。
func (s *Store) Environment() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadDotenv()
}
