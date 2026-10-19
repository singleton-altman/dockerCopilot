package utiles

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/onlyLTY/dockerCopilot/internal/svc"
)

const (
	defaultLogTail = 300
	maxLogTail     = 5000
)

// ContainerLogs 读取容器日志并返回纯文本。
// 非 TTY 容器的日志是 stdout/stderr 多路复用流，必须用 stdcopy 解包，
// 否则输出里会混入二进制帧头。
func ContainerLogs(ctx *svc.ServiceContext, id string, tail int, timestamps bool) (string, error) {
	if id == "" {
		return "", errors.New("容器 id 不能为空")
	}
	if tail <= 0 {
		tail = defaultLogTail
	}
	if tail > maxLogTail {
		tail = maxLogTail
	}

	ctx.DockerClient.NegotiateAPIVersion(context.Background())
	reader, err := ctx.DockerClient.ContainerLogs(context.Background(), id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       strconv.Itoa(tail),
		Timestamps: timestamps,
	})
	if err != nil {
		return "", err
	}
	defer reader.Close()

	// TTY 容器的日志没有多路复用帧头，直接读取即可。
	info, inspectErr := GetContainerInspect(ctx, id)
	if inspectErr != nil {
		return "", inspectErr
	}

	var out bytes.Buffer
	if info.Config != nil && info.Config.Tty {
		if _, err := out.ReadFrom(reader); err != nil {
			return "", err
		}
		return out.String(), nil
	}

	var stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &stderr, reader); err != nil {
		return "", err
	}
	// stderr 追加在后面，便于排查启动失败类问题。
	if stderr.Len() > 0 {
		out.WriteByte('\n')
		out.WriteString(stderr.String())
	}
	return out.String(), nil
}

// ParseLogTail 解析客户端传入的 tail 参数，非法值回落到默认值。
func ParseLogTail(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultLogTail
	}
	// 允许 "all" 表示尽可能多，但仍受上限保护。
	if strings.EqualFold(raw, "all") {
		return maxLogTail
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return defaultLogTail
	}
	if value > maxLogTail {
		return maxLogTail
	}
	return value
}

// NormalizeLogOutput 统一换行符，避免客户端渲染时出现多余的 \r。
func NormalizeLogOutput(raw string) string {
	return strings.ReplaceAll(raw, "\r\n", "\n")
}
