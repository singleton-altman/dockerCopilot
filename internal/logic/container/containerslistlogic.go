package container

import (
	"context"
	"github.com/onlyLTY/dockerCopilot/internal/utiles"
	"strings"
	"time"

	"github.com/onlyLTY/dockerCopilot/internal/svc"
	"github.com/onlyLTY/dockerCopilot/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type ContainersListLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

type Info struct {
	Id          string `json:"id"`
	Status      string `json:"status"`
	Name        string `json:"name"`
	UsingImage  string `json:"usingImage"`
	CreateImage string `json:"createImage"`
	CreateTime  string `json:"createTime"`
	RunningTime string `json:"runningTime"`
	HaveUpdate  bool   `json:"haveUpdate"`
	// UsingImage 为镜像 ID（sha256:…）时为 true：此时无法推导仓库名与标签，
	// 且该引用不能直接用于拉取。客户端据此降级 logo 解析与更新预填。
	UsingImageIsID bool `json:"usingImageIsID"`
	// Compose 项目与服务的标签，非 compose 创建的容器为空。
	ComposeProject string `json:"composeProject,omitempty"`
	ComposeService string `json:"composeService,omitempty"`
}

func NewContainersListLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ContainersListLogic {
	return &ContainersListLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *ContainersListLogic) ContainersList() (resp *types.Resp, err error) {
	// 获取所有容器（包括停止的容器）
	resp = &types.Resp{}
	list, err := utiles.GetContainerList(l.svcCtx)
	if err != nil {
		resp.Code = 500
		resp.Msg = err.Error()
		resp.Data = map[string]interface{}{}
		return resp, err
	}
	resp.Msg = "success"
	var containerInfoList []Info
	list = utiles.CheckImageUpdate(l.svcCtx, list)
	for _, v := range list {
		var containerInfo Info
		containerInfo.Id = v.ID
		containerInfo.Status = v.State
		if len(v.Names) > 0 {
			ContainerName := v.Names[0][1:]
			containerInfo.Name = ContainerName
		} else {
			containerInfo.Name = "get container name error"
			l.Error("get container name error" + v.ID)
		}
		// 容器引用的 tag 被重新 pull / compose build 指向别的镜像后，
		// daemon 列表接口对该容器直接返回镜像 ID（sha256:…）而不是 tag。
		// 对这种容器补一次 inspect 取回 Config.Image 的原始 tag，
		// 避免列表显示 sha、更新表单预填出无法 pull 的镜像 ID；
		// 正常容器仍走批量路径，不引入逐容器 inspect 的 N+1。
		usingImage := v.Image
		if usingImage == "" {
			usingImage = v.ImageID
			l.Error("image dont have name" + v.ID)
		}
		usingImageIsID := strings.HasPrefix(usingImage, "sha256:")
		if usingImageIsID {
			if inspected, err := l.svcCtx.DockerClient.ContainerInspect(l.ctx, v.ID); err == nil &&
				inspected.Config != nil && inspected.Config.Image != "" {
				usingImage = inspected.Config.Image
				usingImageIsID = strings.HasPrefix(usingImage, "sha256:")
			}
		}
		containerInfo.UsingImage = usingImage
		containerInfo.UsingImageIsID = usingImageIsID
		// CreateImage 与 UsingImage 取自同一个来源：
		// 列表接口的 Image 就是 inspect 的 Config.Image，仅在 tag 失效时二者才有差别。
		// 此前对每个容器额外调用一次 inspect，只为取这个字符串，
		// 使列表接口耗时随容器数量线性增长；且 inspect 失败时
		// 仍会解引用零值 Config 导致 panic。
		containerInfo.CreateImage = usingImage
		// compose 创建的容器带 com.docker.compose.project / .service 标签
		if v.Labels != nil {
			containerInfo.ComposeProject = v.Labels["com.docker.compose.project"]
			containerInfo.ComposeService = v.Labels["com.docker.compose.service"]
		}
		t := time.Unix(v.Created, 0)
		containerInfo.CreateTime = t.Format("2006-01-02 15:04:05")
		containerInfo.RunningTime = v.Status
		containerInfo.HaveUpdate = v.Update
		containerInfoList = append(containerInfoList, containerInfo)
	}
	resp.Data = containerInfoList
	return resp, nil
}
