# dockerCopilot

<a href="https://www.gnu.org/licenses/agpl-3.0.en.html">
  <img alt="License: AGPLv3" src="https://shields.io/badge/License-AGPL%20v3-blue.svg">
</a>

一个主打便捷的 Docker 容器管理工具，提供服务端 API、Web 管理界面和移动端客户端。

## 功能

**容器**

- 一键更新容器，支持指定镜像与 tag
- 启动、停止、重启、重命名容器
- 查看容器日志，支持 tail、时间戳、结构化日志解析
- 更新进度查看

**镜像**

- 删除无 TAG 镜像
- 删除未使用镜像

**备份**

- 备份容器配置
- 恢复容器配置
- 导出为 compose YAML

**Compose 文件管理**

- 在线编辑 compose 文件，保存前做结构校验
- 保存前自动备份，支持版本历史与一键回滚
- 删除走回收站，可恢复
- 投递 `up` / `down` / `restart` / `pull` 执行请求

**关注仓库发布监控**

- 关注 GitHub 仓库，定时检查最新 Release
- 记录每个仓库的最近版本与检查状态

## 使用

### Docker Compose 安装

```yaml
services:
  dockercopilot:
    container_name: dockercopilot
    restart: always
    network_mode: bridge
    ports:
      - 12712:12712
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      # 数据目录：compose 文件、备份、历史都保存在这里
      - ./data:/data
    environment:
      - TZ=Asia/Shanghai
      - DOCKER_HOST=unix:///var/run/docker.sock
      - secretKey=密码，不少于八位且非纯数字
      # 可选：关注仓库监控
      - RELEASE_WATCH_INTERVAL_MINUTES=60
      - GITHUB_TOKEN=
    image: 0nlylty/dockercopilot:latest
```

访问 `http://<host>:12712/manager` 打开管理界面。

### 配置项

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `secretKey` | 必填 | 登录密码，不少于 8 位且非纯数字 |
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker daemon 地址 |
| `BACKUP_DIR` | `/data/backups` | 容器备份目录 |
| `COMPOSE_DIR` | `/data/stacks` | compose 文件目录 |
| `RELEASE_WATCH_INTERVAL_MINUTES` | `60` | 发布检查间隔，最小 5 分钟 |
| `GITHUB_TOKEN` | 空 | 提高 GitHub API 频率上限 |
| `DelOldContainer` | `true` | 更新容器后是否删除旧容器 |

## API

所有接口位于 `/api` 下，除 `/auth` 外都需要 `Authorization: Bearer <jwt>`。

### 认证

```
POST /api/auth    form: secretKey
```

### 容器

```
GET    /api/containers
POST   /api/container/:id/start
POST   /api/container/:id/stop
POST   /api/container/:id/restart
POST   /api/container/:id/rename?newName=
POST   /api/container/:id/update
GET    /api/container/:id/logs?tail=&timestamps=
GET    /api/container/backup
GET    /api/container/backup2compose
GET    /api/container/listBackups
POST   /api/container/backups/restore
DELETE /api/container/backups?filename=
```

`GET /api/containers` 额外返回 `composeProject`、`composeService` 和 `usingImageIsID` 字段。
`usingImageIsID` 为 `true` 时表示 `usingImage` 只是镜像 ID，客户端不应按 `name:tag` 解析。

### 镜像

```
GET    /api/images
DELETE /api/image/:id?force=
```

### Compose 文件

```
GET    /api/compose/files
GET    /api/compose/file?filename=
PUT    /api/compose/file           {"filename","content"}
DELETE /api/compose/file?filename=
POST   /api/compose/validate       {"filename","content"}
GET    /api/compose/versions?filename=
POST   /api/compose/rollback       {"filename","backupName"}
GET    /api/compose/trash
POST   /api/compose/restore        {"name"}
```

### Compose 执行

```
POST /api/compose/deploy    {"filename","action"}   action: up|down|restart|pull
GET  /api/compose/job?jobId=
GET  /api/compose/history
```

执行采用**投递式**设计：服务端只把请求写入队列，不直接调用 docker。
实际执行由宿主机侧的 watcher 完成，详见下方「Compose 执行」。

### 发布监控

```
GET    /api/release/repos
POST   /api/release/repos      {"repo":"owner/repo"}
DELETE /api/release/repos      {"repo":"owner/repo"}
POST   /api/release/check
```

### 其他

```
GET /api/version?type=local
GET /api/progress/:taskid
```

## Compose 文件存储与备份

compose 文件存放在 `COMPOSE_DIR`（默认 `/data/stacks`），目录结构：

```
/data/stacks/
├── app.yaml                 # compose 文件
├── .backups/                # 保存前自动备份，每文件最多保留 20 份
├── .trash/                  # 软删除的文件，可恢复
├── .history.jsonl           # 版本历史，最多 200 条
├── .queue/                  # 待执行请求（服务端写入）
└── .results/                # 执行结果（watcher 写入）
```

- 覆盖保存前会先备份旧内容，**备份失败则取消保存**
- 删除是软删除，进入 `.trash` 可从界面或接口恢复
- 回滚前会把当前内容再备份一次，避免回滚成为新的丢失点
- 文件名校验：仅允许单层 `.yml` / `.yaml`，拒绝路径穿越

## Compose 执行

服务端容器只挂载 `docker.sock`，没有 compose 二进制，也不应直接执行 compose 命令。
因此执行拆成两段：

```
客户端 → 服务端（写 .queue/{jobId}.json）
                  ↓
         宿主机 watcher 轮询
                  ↓
         docker compose -f ... up -d
                  ↓
         写 .results/{jobId}.json
                  ↓
客户端 ← GET /api/compose/job?jobId=
```

在宿主机上启动 watcher：

```bash
COMPOSE_WATCH_DIR=/path/to/data/stacks ./compose-watcher.sh
```

watcher 的行为：

- 只接受 `up` / `down` / `restart` / `pull`
- 二次校验文件名与动作白名单
- 超过 5 分钟的请求视为过期，不执行
- 使用 `--project-directory` 指定 stacks 目录，保证相对路径一致
- 结果只保留最近 50 条

**注意**：watcher 停止时，界面上的执行按钮只会写入队列文件，不会真正执行。
生产环境建议用 systemd / launchd 托管。

## 安全说明

- 挂载 `docker.sock` 等价于宿主机 root 权限，**不要暴露到公网**
- compose 文件与执行请求都做文件名、动作白名单校验
- 关注仓库的请求地址固定为 `https://api.github.com`
- compose 执行的实际权限由宿主机 watcher 承担，服务端不执行任意命令

## 开发环境

Go 1.23+。

```bash
go run dockercopilot.go
```

运行测试：

```bash
go test ./internal/...
```
