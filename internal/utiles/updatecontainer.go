package utiles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	dockerMsgType "github.com/docker/docker/pkg/jsonmessage"
	"github.com/google/uuid"
	"github.com/onlyLTY/dockerCopilot/internal/module"
	"github.com/onlyLTY/dockerCopilot/internal/svc"
	"github.com/zeromicro/go-zero/core/logx"
)

var ErrUpdateInProgress = errors.New("该容器正在更新中")

// ResolveRecordName 把路径参数解析成记录键（容器名）。
// 客户端可能传容器 ID，此时通过 inspect 取回名称，兼容两种调用方式。
func ResolveRecordName(ctx *svc.ServiceContext, idOrName string) string {
	if _, ok := ctx.UpdateRecords.Get(idOrName); ok {
		return idOrName
	}
	if inspected, err := ctx.DockerClient.ContainerInspect(context.Background(), idOrName); err == nil {
		name := inspected.Name
		if len(name) > 0 && name[0] == '/' {
			name = name[1:]
		}
		if _, ok := ctx.UpdateRecords.Get(name); ok {
			return name
		}
	}
	return idOrName
}

// setPhase 推进阶段并落盘。
func setPhase(serviceContext *svc.ServiceContext, rec *module.ContainerUpdateRecord, phase, message string, percent int) {
	rec.Phase = phase
	serviceContext.UpdateRecords.Update(rec.ContainerName, func(r *module.ContainerUpdateRecord) {
		r.Phase = phase
	})
	serviceContext.UpdateProgress(rec.ID, svc.TaskProgress{
		TaskID:     rec.ID,
		Percentage: percent,
		Name:       rec.ContainerName,
		Message:    message,
		DetailMsg:  message,
		IsDone:     false,
	})
}

// watchCancel 每 200ms 检查一次取消意图，一旦置位立即取消 context，
// 使正在阻塞的镜像下载马上返回，而不必等下载完成。
func watchCancel(ctx *svc.ServiceContext, name string, cancel context.CancelFunc) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		if ctx.UpdateRecords.IsCancelRequested(name) {
			cancel()
			return
		}
		latest, ok := ctx.UpdateRecords.Snapshot(name)
		if !ok || latest.Status != module.StatusRunning {
			cancel()
			return
		}
	}
}

// isCanceledErr 判断错误是否来自 context 取消（用户中断拉取）。
func isCanceledErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "operation was canceled")
}

// BeginUpdate 校验并发并创建更新记录。返回记录，后续由 UpdateContainer 推进。
func BeginUpdate(ctx *svc.ServiceContext, id, name, imageNameAndTag string) (*module.ContainerUpdateRecord, error) {
	if existing, ok := ctx.UpdateRecords.Get(name); ok && existing.Status == module.StatusRunning {
		return nil, ErrUpdateInProgress
	}
	inspected, err := ctx.DockerClient.ContainerInspect(context.Background(), id)
	if err != nil {
		return nil, err
	}
	originalName := inspected.Name
	if len(originalName) > 0 && originalName[0] == '/' {
		originalName = originalName[1:]
	}
	rec := &module.ContainerUpdateRecord{
		ID:              uuid.New().String(),
		ContainerName:   name,
		OriginalID:      inspected.ID,
		OriginalName:    originalName,
		OriginalImage:   inspected.Config.Image,
		ImageNameAndTag: imageNameAndTag,
		WasRunning:      inspected.State != nil && inspected.State.Running,
		Phase:           module.PhasePulling,
		Status:          module.StatusRunning,
	}
	ctx.UpdateRecords.Begin(rec)
	return rec, nil
}

// UpdateContainer 推进一次容器更新。
//
// 中断语义：只在镜像拉取阶段可中断（此时容器未被改动，直接停止即可）；
// 进入容器变更阶段（停止/改名/重建）后不可中断，更新会执行到底，
// 任一步失败都会把旧容器改回原名并恢复运行，不会留在半截状态。
func UpdateContainer(serviceContext *svc.ServiceContext, rec *module.ContainerUpdateRecord, delOldContainer bool) error {
	// 可取消的 context：cancel 接口置位后，正在进行的 ImagePull 读取会被立即打断。
	ctx, cancelUpdate := context.WithCancel(context.Background())
	defer cancelUpdate()
	go watchCancel(serviceContext, rec.ContainerName, cancelUpdate)

	cli := serviceContext.DockerClient
	name := rec.ContainerName

	fail := func(msg string, err error) error {
		serviceContext.UpdateRecords.Update(name, func(r *module.ContainerUpdateRecord) {
			r.Status = module.StatusFailed
			r.Phase = module.PhaseFailed
			r.Error = msg
		})
		serviceContext.UpdateProgress(rec.ID, svc.TaskProgress{
			TaskID: rec.ID, Percentage: 0, Name: name,
			Message: msg, DetailMsg: err.Error(), IsDone: true,
		})
		logx.Errorf("更新容器 %s 失败: %s: %v", name, msg, err)
		return err
	}

	// 1. 拉取镜像（唯一可中断的阶段）
	setPhase(serviceContext, rec, module.PhasePulling, "正在拉取新镜像", 10)
	reader, err := cli.ImagePull(ctx, rec.ImageNameAndTag, image.PullOptions{})
	if err != nil {
		if isCanceledErr(err) || serviceContext.UpdateRecords.IsCancelRequested(name) {
			return abortCanceled(serviceContext, rec)
		}
		return fail("拉取镜像失败", err)
	}
	if err := decodePullResp(reader, serviceContext, rec.ID); err != nil {
		if isCanceledErr(err) || serviceContext.UpdateRecords.IsCancelRequested(name) {
			return abortCanceled(serviceContext, rec)
		}
		return fail("拉取镜像失败", err)
	}

	// 2. 停止旧容器
	setPhase(serviceContext, rec, module.PhaseStopped, "正在停止容器", 30)
	timeout := 10
	if err := cli.ContainerStop(ctx, rec.OriginalID, container.StopOptions{
		Signal: "SIGINT", Timeout: &timeout,
	}); err != nil {
		return fail("停止容器失败", err)
	}

	// 3. 重命名旧容器
	setPhase(serviceContext, rec, module.PhaseRenamed, "正在备份旧容器", 40)
	renamedTo := name + "-" + time.Now().Format("2006-01-02-15-04-05")
	if err := cli.ContainerRename(ctx, rec.OriginalID, renamedTo); err != nil {
		return fail("重命名旧容器失败", err)
	}
	serviceContext.UpdateRecords.Update(name, func(r *module.ContainerUpdateRecord) {
		r.RenamedTo = renamedTo
	})
	rec.RenamedTo = renamedTo

	// 4. 用新镜像创建同名容器
	setPhase(serviceContext, rec, module.PhaseCreated, "正在创建新容器", 60)
	inspected, err := cli.ContainerInspect(ctx, rec.OriginalID)
	if err != nil {
		return failWithRestore(serviceContext, rec, renamedTo, "获取容器信息失败", err)
	}
	config := inspected.Config
	config.Hostname = ""
	config.Image = rec.ImageNameAndTag
	networkingConfig := &network.NetworkingConfig{
		EndpointsConfig: inspected.NetworkSettings.Networks,
	}
	created, err := cli.ContainerCreate(ctx, config, inspected.HostConfig, networkingConfig, nil, name)
	if err != nil {
		return failWithRestore(serviceContext, rec, renamedTo, "创建新容器失败", err)
	}
	serviceContext.UpdateRecords.Update(name, func(r *module.ContainerUpdateRecord) {
		r.NewContainerID = created.ID
	})
	rec.NewContainerID = created.ID

	// 5. 启动新容器
	setPhase(serviceContext, rec, module.PhaseStarted, "正在启动新容器", 80)
	if err := cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return failWithRestore(serviceContext, rec, renamedTo, "启动新容器失败", err)
	}

	// 6. 完成：按 DelOldContainer 决定旧容器去留
	setPhase(serviceContext, rec, module.PhaseDone, "更新成功", 100)
	serviceContext.UpdateRecords.Update(name, func(r *module.ContainerUpdateRecord) {
		r.Status = module.StatusSucceeded
		r.Phase = module.PhaseDone
	})
	if delOldContainer {
		if err := cli.ContainerRemove(ctx, rec.OriginalID, container.RemoveOptions{}); err != nil {
			logx.Errorf("删除旧容器 %s 失败: %v", renamedTo, err)
		}
	}
	serviceContext.UpdateProgress(rec.ID, svc.TaskProgress{
		TaskID: rec.ID, Percentage: 100, Name: name,
		Message: "更新成功", DetailMsg: "更新成功", IsDone: true,
	})
	return nil
}

// abortCanceled 拉取阶段的中断：容器未被改动，直接标记取消。
func abortCanceled(serviceContext *svc.ServiceContext, rec *module.ContainerUpdateRecord) error {
	serviceContext.UpdateRecords.Update(rec.ContainerName, func(r *module.ContainerUpdateRecord) {
		r.Status = module.StatusCanceled
		r.Phase = module.PhaseCanceled
		r.Error = "用户中断更新"
		r.CancelRequested = false
	})
	rec.Status = module.StatusCanceled
	rec.Phase = module.PhaseCanceled
	serviceContext.UpdateProgress(rec.ID, svc.TaskProgress{
		TaskID: rec.ID, Percentage: 0, Name: rec.ContainerName,
		Message: "已中断更新（容器未做任何改动）", DetailMsg: "用户中断更新", IsDone: true,
	})
	return nil
}

// failWithRestore 变更阶段的失败恢复：把旧容器改回原名并恢复运行。
// 只做改名还原（不做重建），足以消除该阶段的半截状态。
func failWithRestore(serviceContext *svc.ServiceContext, rec *module.ContainerUpdateRecord, renamedTo, msg string, err error) error {
	bg := context.Background()
	restoreErr := error(nil)
	if renamedTo != "" {
		if e := serviceContext.DockerClient.ContainerRename(bg, rec.OriginalID, rec.ContainerName); e != nil {
			restoreErr = fmt.Errorf("恢复旧容器名失败: %v", e)
		}
	}
	if restoreErr == nil && rec.WasRunning {
		if e := serviceContext.DockerClient.ContainerStart(bg, rec.OriginalID, container.StartOptions{}); e != nil {
			// 已在运行的容器报 already running，不是错误
			if !strings.Contains(e.Error(), "already running") {
				restoreErr = fmt.Errorf("恢复旧容器运行失败: %v", e)
			}
		}
	}
	if restoreErr != nil {
		msg = msg + "；" + restoreErr.Error()
	}
	serviceContext.UpdateRecords.Update(rec.ContainerName, func(r *module.ContainerUpdateRecord) {
		r.Status = module.StatusFailed
		r.Phase = module.PhaseFailed
		r.Error = msg
	})
	serviceContext.UpdateProgress(rec.ID, svc.TaskProgress{
		TaskID: rec.ID, Percentage: 0, Name: rec.ContainerName,
		Message: msg, DetailMsg: err.Error(), IsDone: true,
	})
	logx.Errorf("更新容器 %s 失败: %s: %v", rec.ContainerName, msg, err)
	return err
}

// decodePullResp 消费拉取进度流，把状态写进任务进度。
func decodePullResp(reader io.Reader, ctx *svc.ServiceContext, taskID string) (err error) {
	decoder := json.NewDecoder(reader)
	for {
		var msg dockerMsgType.JSONMessage
		if err = decoder.Decode(&msg); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("拉取镜像失败: %w", err)
		}
		if msg.Error != nil {
			return fmt.Errorf("拉取镜像失败: %w", msg.Error)
		}
		detail := fmt.Sprintf("进度%s", msg.Status)
		if msg.Progress != nil {
			detail = fmt.Sprintf("进度%s: %s", msg.Status, msg.Progress.String())
		}
		ctx.UpdateProgress(taskID, svc.TaskProgress{
			TaskID: taskID, Percentage: 25, Message: detail, DetailMsg: detail,
		})
	}
}
