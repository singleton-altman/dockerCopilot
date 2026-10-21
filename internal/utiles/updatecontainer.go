package utiles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	dockerTypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	dockerMsgType "github.com/docker/docker/pkg/jsonmessage"
	"github.com/google/uuid"
	"github.com/onlyLTY/dockerCopilot/internal/module"
	"github.com/onlyLTY/dockerCopilot/internal/svc"
	"github.com/zeromicro/go-zero/core/logx"
)

// 旧容器在更新成功后保留的时长，用于事后回撤。
const rollbackRetention = 24 * time.Hour

var (
	ErrUpdateInProgress   = errors.New("该容器正在更新中")
	ErrRollbackNotAllowed = errors.New("该记录当前不可回撤")
)

// BeginUpdate 校验并发并创建更新记录。返回记录 ID，后续由 UpdateContainer 推进。
func BeginUpdate(ctx *svc.ServiceContext, id, name, imageNameAndTag string) (*module.ContainerUpdateRecord, error) {
	if existing, ok := ctx.UpdateRecords.Get(name); ok && existing.Status == module.StatusRunning {
		return nil, ErrUpdateInProgress
	}
	inspected, err := ctx.DockerClient.ContainerInspect(context.Background(), id)
	if err != nil {
		return nil, err
	}
	rec := &module.ContainerUpdateRecord{
		ID:              uuid.New().String(),
		ContainerName:   name,
		OriginalID:      inspected.ID,
		OriginalName:    containerName(inspected.Name),
		OriginalImage:   inspected.Config.Image,
		ImageNameAndTag: imageNameAndTag,
		WasRunning:      inspected.State != nil && inspected.State.Running,
		ConfigSnapshot:  snapshotContainer(inspected),
		Phase:           module.PhasePulling,
		Status:          module.StatusRunning,
	}
	ctx.UpdateRecords.Begin(rec)
	return rec, nil
}

func containerName(inspectName string) string {
	if len(inspectName) > 0 && inspectName[0] == '/' {
		return inspectName[1:]
	}
	return inspectName
}

// snapshotContainer 保存旧容器的重建所需配置（容器被删后回撤要靠它）。
func snapshotContainer(inspected dockerTypes.ContainerJSON) *module.ContainerConfigSnapshot {
	cfg, err1 := json.Marshal(inspected.Config)
	host, err2 := json.Marshal(inspected.HostConfig)
	networks := &network.NetworkingConfig{}
	if inspected.NetworkSettings != nil {
		networks.EndpointsConfig = inspected.NetworkSettings.Networks
	}
	netCfg, err3 := json.Marshal(networks)
	if err1 != nil || err2 != nil || err3 != nil {
		logx.Errorf("保存容器配置快照失败: %v %v %v", err1, err2, err3)
		return nil
	}
	return &module.ContainerConfigSnapshot{Config: cfg, HostConfig: host, NetworkingConfig: netCfg}
}

// setPhase 推进阶段并落盘。
func setPhase(ctx *svc.ServiceContext, rec *module.ContainerUpdateRecord, phase, message string, percent int) {
	rec.Phase = phase
	ctx.UpdateRecords.Update(rec.ContainerName, func(r *module.ContainerUpdateRecord) {
		r.Phase = phase
	})
	ctx.UpdateProgress(rec.ID, svc.TaskProgress{
		TaskID:     rec.ID,
		Percentage: percent,
		Name:       rec.ContainerName,
		Message:    message,
		DetailMsg:  message,
		IsDone:     false,
	})
}

// canceled 检查取消意图；取消由 cancel 接口置位。
func canceled(ctx *svc.ServiceContext, rec *module.ContainerUpdateRecord) bool {
	return ctx.UpdateRecords.IsCancelRequested(rec.ContainerName)
}

// UpdateContainer 推进一次容器更新。每个阶段之间检查取消信号；
// 取消或任一步失败都会自动回撤到更新前状态，不会留在半截。
func UpdateContainer(serviceContext *svc.ServiceContext, rec *module.ContainerUpdateRecord, delOldContainer bool) error {
	ctx := context.Background()
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

	// 1. 拉取镜像
	setPhase(serviceContext, rec, module.PhasePulling, "正在拉取新镜像", 10)
	reader, err := cli.ImagePull(ctx, rec.ImageNameAndTag, image.PullOptions{})
	if err != nil {
		return fail("拉取镜像失败", err)
	}
	if err := decodePullResp(reader, serviceContext, rec.ID); err != nil {
		return fail("拉取镜像失败", err)
	}
	if canceled(serviceContext, rec) {
		return abortAndRollback(serviceContext, rec, "用户中断更新", 25)
	}

	// 2. 停止旧容器（记录 wasRunning，回撤时按原状态恢复）
	setPhase(serviceContext, rec, module.PhaseStopped, "正在停止容器", 30)
	timeout := 10
	if err := cli.ContainerStop(ctx, rec.OriginalID, container.StopOptions{
		Signal: "SIGINT", Timeout: &timeout,
	}); err != nil {
		return fail("停止容器失败", err)
	}
	if canceled(serviceContext, rec) {
		return abortAndRollback(serviceContext, rec, "用户中断更新", 35)
	}

	// 3. 重命名旧容器（保留而非删除，供回撤）
	setPhase(serviceContext, rec, module.PhaseRenamed, "正在备份旧容器", 40)
	renamedTo := name + "-" + time.Now().Format("2006-01-02-15-04-05")
	if err := cli.ContainerRename(ctx, rec.OriginalID, renamedTo); err != nil {
		return fail("重命名旧容器失败", err)
	}
	serviceContext.UpdateRecords.Update(name, func(r *module.ContainerUpdateRecord) {
		r.RenamedTo = renamedTo
	})
	rec.RenamedTo = renamedTo
	if canceled(serviceContext, rec) {
		return abortAndRollback(serviceContext, rec, "用户中断更新", 50)
	}

	// 4. 用新镜像创建同名容器
	setPhase(serviceContext, rec, module.PhaseCreated, "正在创建新容器", 60)
	inspected, err := cli.ContainerInspect(ctx, rec.OriginalID)
	if err != nil {
		return abortAndRollback(serviceContext, rec, "获取容器信息失败", 55)
	}
	config := inspected.Config
	config.Hostname = ""
	config.Image = rec.ImageNameAndTag
	networkingConfig := &network.NetworkingConfig{
		EndpointsConfig: inspected.NetworkSettings.Networks,
	}
	created, err := cli.ContainerCreate(ctx, config, inspected.HostConfig, networkingConfig, nil, name)
	if err != nil {
		return abortAndRollback(serviceContext, rec, "创建新容器失败", 60)
	}
	serviceContext.UpdateRecords.Update(name, func(r *module.ContainerUpdateRecord) {
		r.NewContainerID = created.ID
	})
	rec.NewContainerID = created.ID
	if canceled(serviceContext, rec) {
		return abortAndRollback(serviceContext, rec, "用户中断更新", 70)
	}

	// 5. 启动新容器
	setPhase(serviceContext, rec, module.PhaseStarted, "正在启动新容器", 80)
	if err := cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return abortAndRollback(serviceContext, rec, "启动新容器失败", 80)
	}

	// 6. 完成：旧容器按保留策略处理（默认保留 24 小时供回撤）
	setPhase(serviceContext, rec, module.PhaseDone, "更新成功", 100)
	serviceContext.UpdateRecords.Update(name, func(r *module.ContainerUpdateRecord) {
		r.Status = module.StatusSucceeded
		r.Phase = module.PhaseDone
	})
	if delOldContainer {
		// 只在确认新容器健康后延时清理；这里先保留，由 CleanupExpired 到期删除。
		logx.Infof("旧容器 %s 保留 %s 供回撤", renamedTo, rollbackRetention)
	}
	serviceContext.UpdateProgress(rec.ID, svc.TaskProgress{
		TaskID: rec.ID, Percentage: 100, Name: name,
		Message: "更新成功", DetailMsg: "更新成功", IsDone: true,
	})
	return nil
}

// abortAndRollback 中断更新并回撤，保证不留在半截状态。
func abortAndRollback(ctx *svc.ServiceContext, rec *module.ContainerUpdateRecord, reason string, percent int) error {
	ctx.UpdateProgress(rec.ID, svc.TaskProgress{
		TaskID: rec.ID, Percentage: percent, Name: rec.ContainerName,
		Message: reason + "，正在回撤", DetailMsg: reason, IsDone: false,
	})
	return Rollback(ctx, rec.ContainerName, module.StatusCanceled, reason)
}

// Rollback 按记录所处阶段逆序恢复到更新前状态，幂等。
// finalStatus 用于区分「用户取消」与「失败后回撤」。
func Rollback(ctx *svc.ServiceContext, name, finalStatus, reason string) error {
	rec, ok := ctx.UpdateRecords.Snapshot(name)
	if !ok {
		return fmt.Errorf("未找到容器 %s 的更新记录", name)
	}
	if !rec.CanRollback() {
		return ErrRollbackNotAllowed
	}
	cli := ctx.DockerClient
	bg := context.Background()

	ctx.UpdateRecords.Update(name, func(r *module.ContainerUpdateRecord) {
		r.Phase = module.PhaseRolling
	})

	// 1. 删除更新过程中创建的新容器
	if rec.NewContainerID != "" {
		_ = cli.ContainerStop(bg, rec.NewContainerID, container.StopOptions{Timeout: intPtr(5)})
		if err := cli.ContainerRemove(bg, rec.NewContainerID, container.RemoveOptions{Force: true}); err != nil {
			logx.Errorf("回撤: 删除新容器 %s 失败: %v", rec.NewContainerID, err)
		}
	}
	// 2. 恢复旧容器：还在（已改名）就改回原名，否则按快照重建
	if err := restoreOriginalContainer(ctx, &rec); err != nil {
		ctx.UpdateRecords.Update(name, func(r *module.ContainerUpdateRecord) {
			r.Status = module.StatusFailed
			r.Phase = module.PhaseFailed
			r.Error = "回撤失败: " + err.Error()
		})
		return err
	}

	// 3. 恢复更新前的运行状态
	if rec.WasRunning {
		if err := cli.ContainerStart(bg, rec.ContainerName, container.StartOptions{}); err != nil {
			ctx.UpdateRecords.Update(name, func(r *module.ContainerUpdateRecord) {
				r.Status = module.StatusFailed
				r.Error = "回撤后启动容器失败: " + err.Error()
			})
			return err
		}
	}

	ctx.UpdateRecords.Update(name, func(r *module.ContainerUpdateRecord) {
		r.Status = finalStatus
		r.Phase = module.PhaseRolledBck
		r.CancelRequested = false
		r.Error = reason
	})
	ctx.UpdateProgress(rec.ID, svc.TaskProgress{
		TaskID: rec.ID, Percentage: 100, Name: name,
		Message: "已回撤到更新前版本", DetailMsg: reason, IsDone: true,
	})
	return nil
}

// restoreOriginalContainer 把旧容器恢复到原名并确保存在。
func restoreOriginalContainer(ctx *svc.ServiceContext, rec *module.ContainerUpdateRecord) error {
	cli := ctx.DockerClient
	bg := context.Background()

	// 旧容器还在：改名回原名即可
	if rec.RenamedTo != "" {
		if _, err := cli.ContainerInspect(bg, rec.RenamedTo); err == nil {
			// 原名若被残留容器占用，先移除残留（理论上新容器已删）
			if _, err := cli.ContainerInspect(bg, rec.ContainerName); err == nil {
				_ = cli.ContainerRemove(bg, rec.ContainerName, container.RemoveOptions{Force: true})
			}
			return cli.ContainerRename(bg, rec.RenamedTo, rec.ContainerName)
		}
	}
	// 旧容器已不存在：按快照重建
	if _, err := cli.ContainerInspect(bg, rec.ContainerName); err == nil {
		// 原名容器已存在（新容器删除失败等），先移除
		_ = cli.ContainerRemove(bg, rec.ContainerName, container.RemoveOptions{Force: true})
	}
	snap := rec.ConfigSnapshot
	if snap == nil {
		return errors.New("缺少旧容器配置快照，无法重建")
	}
	var cfg container.Config
	var host container.HostConfig
	var netCfg network.NetworkingConfig
	if err := json.Unmarshal(snap.Config, &cfg); err != nil {
		return fmt.Errorf("解析旧容器配置失败: %w", err)
	}
	if err := json.Unmarshal(snap.HostConfig, &host); err != nil {
		return fmt.Errorf("解析旧容器主机配置失败: %w", err)
	}
	if len(snap.NetworkingConfig) > 0 {
		_ = json.Unmarshal(snap.NetworkingConfig, &netCfg)
	}
	cfg.Hostname = ""
	cfg.Image = rec.OriginalImage
	if _, err := cli.ContainerCreate(bg, &cfg, &host, &netCfg, nil, rec.ContainerName); err != nil {
		return fmt.Errorf("重建旧容器失败: %w", err)
	}
	return nil
}

// CleanupExpired 清理超过保留期的旧容器（`name-时间戳`）并移除对应记录。
// 由启动时与列表接口顺带调用，避免引入常驻 goroutine。
func CleanupExpired(ctx *svc.ServiceContext) {
	now := time.Now()
	for _, rec := range ctx.UpdateRecords.List() {
		if rec.RenamedTo == "" || rec.Status != module.StatusSucceeded {
			continue
		}
		if now.Sub(rec.UpdatedAt) < rollbackRetention {
			continue
		}
		if err := ctx.DockerClient.ContainerRemove(context.Background(), rec.RenamedTo, container.RemoveOptions{Force: true}); err != nil {
			logx.Errorf("清理过期旧容器 %s 失败: %v", rec.RenamedTo, err)
			continue
		}
		// 清空 RenamedTo：记录仍保留在历史里，但不会再被重复清理或回撤。
		ctx.UpdateRecords.Update(rec.ContainerName, func(r *module.ContainerUpdateRecord) {
			r.RenamedTo = ""
		})
		ctx.UpdateRecords.Clear(rec.ContainerName)
		logx.Infof("已清理过期旧容器 %s（超过 %s 保留期）", rec.RenamedTo, rollbackRetention)
	}
}

// ResolveRecordName 把路径参数解析成记录键（容器名）。
// 客户端可能传容器 ID，此时通过 inspect 取回名称，兼容两种调用方式。
func ResolveRecordName(ctx *svc.ServiceContext, idOrName string) string {
	if _, ok := ctx.UpdateRecords.Get(idOrName); ok {
		return idOrName
	}
	if inspected, err := ctx.DockerClient.ContainerInspect(context.Background(), idOrName); err == nil {
		name := containerName(inspected.Name)
		if _, ok := ctx.UpdateRecords.Get(name); ok {
			return name
		}
	}
	return idOrName
}

func intPtr(v int) *int { return &v }

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

var _ = client.Client{}
