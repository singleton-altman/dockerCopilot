package composefile

import (
	"fmt"
	"sort"
	"strings"
)

// 过程日志：把 compose 的原始输出翻译成用户能看懂的阶段结论。
//
// compose pull 的输出形如：
//
//	 Image nginx:alpine Pulling
//	 Image nginx:alpine Pulled
//	 Image dc_local:v1 Error pull access denied for dc_local, ...
//	 Image dc_local:v1 pull access denied for dc_local, ...
//	 Image x Skipped
//
// compose up 的输出形如：
//
//	 Container web Recreate
//	 Container web Recreated
//	 Container web Starting
//	 Container web Started
//	 Container web Running          // 未变化
//	 Container old Removed
//
// 这里只做文本归纳，不额外调用 docker，避免增加执行时间与失败面。

// imagePullOutcome 单个镜像的拉取结果。
type imagePullOutcome struct {
	Image  string
	Status string // pulled / failed / skipped
	Detail string
}

// parsePullOutput 从 pull 输出里提取每个镜像的最终结果。
// 同一镜像可能出现多行，以最后一条有结论的行为准。
func parsePullOutput(output string) []imagePullOutcome {
	order := make([]string, 0, 8)
	byImage := make(map[string]*imagePullOutcome)

	record := func(image, status, detail string) {
		if image == "" {
			return
		}
		item, ok := byImage[image]
		if !ok {
			item = &imagePullOutcome{Image: image}
			byImage[image] = item
			order = append(order, image)
		}
		// 有结论的状态覆盖中间态（Pulling）
		item.Status = status
		if detail != "" {
			item.Detail = detail
		}
	}

	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "time=") {
			continue
		}
		// 去掉行首的 "Image " 前缀
		line = strings.TrimPrefix(line, "Image ")
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// "Image Error pull access denied..." 这类行没有镜像名，直接跳过
		if fields[0] == "Error" || fields[0] == "Container" {
			continue
		}
		image := fields[0]
		rest := strings.TrimSpace(strings.TrimPrefix(line, image))

		// compose 有时把错误单独成行（没有 Image 前缀），
		// 形如 "<image> Error pull access denied..." 已在上面处理；
		// 这里再兜一层：以镜像名开头但含 "Error " 的也要归类
		if strings.Contains(rest, "pull access denied") || strings.HasPrefix(rest, "Error ") {
			if isImageReference(image) {
				detail := rest
				if strings.HasPrefix(rest, "Error ") {
					detail = strings.TrimSpace(strings.TrimPrefix(rest, "Error "))
				}
				record(image, "failed", detail)
			}
			continue
		}

		switch {
		case strings.HasPrefix(rest, "Pulling"), strings.HasPrefix(rest, "Waiting"),
			strings.HasPrefix(rest, "Downloading"), strings.HasPrefix(rest, "Extracting"):
			// 中间态，等最终结论
		case strings.HasPrefix(rest, "Pulled"):
			record(image, "pulled", "")
		case strings.HasPrefix(rest, "Skipped"):
			record(image, "skipped", "compose 配置要求不拉取")
		case strings.HasPrefix(rest, "Error "):
			record(image, "failed", strings.TrimSpace(strings.TrimPrefix(rest, "Error ")))
		case strings.Contains(rest, "pull access denied"),
			strings.Contains(rest, "not found"),
			strings.Contains(rest, "unauthorized"):
			record(image, "failed", rest)
		}
	}

	out := make([]imagePullOutcome, 0, len(order))
	for _, image := range order {
		out = append(out, *byImage[image])
	}
	return out
}

// isImageReference 粗判一个 token 是否像镜像引用（含仓库/标签/摘要字符）。
// 用来排除 "Error"、"Container" 这类被误当成镜像名的关键字。
func isImageReference(token string) bool {
	if token == "" {
		return false
	}
	if token == "Error" || token == "Container" || token == "Network" || token == "Volume" {
		return false
	}
	for _, r := range token {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == ':', r == '/', r == '.', r == '-', r == '_', r == '@':
		default:
			return false
		}
	}
	return true
}

// summarizePull 把拉取结果整理成一条用户可读的结论。
func summarizePull(output string) (step JobStep, outcomes []imagePullOutcome) {
	outcomes = parsePullOutput(output)

	var pulled, failed, skipped []string
	for _, item := range outcomes {
		switch item.Status {
		case "pulled":
			pulled = append(pulled, item.Image)
		case "failed":
			failed = append(failed, item.Image)
		case "skipped":
			skipped = append(skipped, item.Image)
		}
	}

	step.Stage = "pull"
	switch {
	case len(outcomes) == 0:
		step.Status = "warning"
		step.Message = "拉取阶段没有可识别的镜像输出，请查看原始输出"
	case len(failed) == 0:
		step.Status = "success"
		msg := fmt.Sprintf("已拉取 %d 个镜像", len(pulled))
		if len(skipped) > 0 {
			msg += fmt.Sprintf("，%d 个按配置跳过", len(skipped))
		}
		step.Message = msg
	default:
		step.Status = "warning"
		// 不在这里断言「将使用本地镜像」：纯 pull 动作不会启动容器，
		// 这个结论只在 deploy 的上下文中成立，由调用方补充。
		step.Message = fmt.Sprintf("%d 个镜像拉取失败（%s）",
			len(failed), strings.Join(failed, "、"))
		if len(pulled) > 0 {
			step.Message = fmt.Sprintf("已拉取 %d 个镜像；%d 个拉取失败（%s）",
				len(pulled), len(failed), strings.Join(failed, "、"))
		}
	}
	return step, outcomes
}

// containerUpOutcome 单个容器的启动结果。
type containerUpOutcome struct {
	Container string
	Status    string // created / recreated / started / unchanged / removed / failed
}

// parseUpOutput 从 up 输出里提取每个容器的最终状态。
func parseUpOutput(output string) []containerUpOutcome {
	order := make([]string, 0, 8)
	byName := make(map[string]*containerUpOutcome)

	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "time=") {
			continue
		}
		line = strings.TrimPrefix(line, "Container ")
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		rest := strings.TrimSpace(strings.TrimPrefix(line, name))

		var status string
		switch {
		case strings.HasPrefix(rest, "Recreated"):
			status = "recreated"
		case strings.HasPrefix(rest, "Recreate"):
			status = "recreated"
		case strings.HasPrefix(rest, "Created"), strings.HasPrefix(rest, "Create"):
			status = "created"
		case strings.HasPrefix(rest, "Started"), strings.HasPrefix(rest, "Starting"):
			status = "started"
		case strings.HasPrefix(rest, "Running"):
			status = "unchanged"
		case strings.HasPrefix(rest, "Removed"), strings.HasPrefix(rest, "Removing"):
			status = "removed"
		case strings.HasPrefix(rest, "Error"):
			status = "failed"
		default:
			continue
		}

		item, ok := byName[name]
		if !ok {
			item = &containerUpOutcome{Container: name}
			byName[name] = item
			order = append(order, name)
		}
		// Recreated 比 Created/Started 更能说明「镜像换了」，保留更强的结论
		if item.Status == "recreated" && status != "recreated" {
			continue
		}
		item.Status = status
	}

	out := make([]containerUpOutcome, 0, len(order))
	for _, name := range order {
		out = append(out, *byName[name])
	}
	return out
}

// summarizeUp 把启动结果整理成一条用户可读的结论。
func summarizeUp(output string) JobStep {
	outcomes := parseUpOutput(output)

	var recreated, created, started, unchanged, removed []string
	for _, item := range outcomes {
		switch item.Status {
		case "recreated":
			recreated = append(recreated, item.Container)
		case "created":
			created = append(created, item.Container)
		case "started":
			started = append(started, item.Container)
		case "unchanged":
			unchanged = append(unchanged, item.Container)
		case "removed":
			removed = append(removed, item.Container)
		}
	}

	step := JobStep{Stage: "up", Status: "success"}
	parts := make([]string, 0, 5)
	if len(recreated) > 0 {
		parts = append(parts, fmt.Sprintf("%d 个容器已重建（%s）", len(recreated), strings.Join(recreated, "、")))
	}
	if len(created) > 0 {
		parts = append(parts, fmt.Sprintf("%d 个容器已创建（%s）", len(created), strings.Join(created, "、")))
	}
	if len(started) > 0 {
		parts = append(parts, fmt.Sprintf("%d 个容器已启动（%s）", len(started), strings.Join(started, "、")))
	}
	if len(unchanged) > 0 {
		parts = append(parts, fmt.Sprintf("%d 个容器无变化（%s）", len(unchanged), strings.Join(unchanged, "、")))
	}
	if len(removed) > 0 {
		parts = append(parts, fmt.Sprintf("%d 个容器已移除（%s）", len(removed), strings.Join(removed, "、")))
	}
	if len(parts) == 0 {
		step.Status = "warning"
		step.Message = "启动阶段没有可识别的容器输出，请查看原始输出"
		return step
	}
	step.Message = strings.Join(parts, "；")
	return step
}

// summarizeSingle 为单动作任务生成一条结论。
func summarizeSingle(action Action, output string, success bool) []JobStep {
	if action == ActionPull {
		step, _ := summarizePull(output)
		// 命令失败就一定是失败，即使输出里认不出是哪个镜像
		if !success {
			step.Status = "failed"
		}
		return []JobStep{step}
	}

	step := summarizeUp(output)
	if !success {
		step.Status = "failed"
	}
	step.Stage = string(action)
	switch action {
	case ActionDown:
		step.Stage = "down"
	case ActionRestart:
		step.Stage = "restart"
	}
	return []JobStep{step}
}

// sortSteps 保证阶段顺序稳定（pull 在 up 之前）。
func sortSteps(steps []JobStep) {
	rank := func(stage string) int {
		switch stage {
		case "pull":
			return 0
		case "up":
			return 1
		}
		return 2
	}
	sort.SliceStable(steps, func(i, j int) bool { return rank(steps[i].Stage) < rank(steps[j].Stage) })
}
