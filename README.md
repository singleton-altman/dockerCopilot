# dockerCopilot

<a href="https://www.gnu.org/licenses/agpl-3.0.en.html">
  <img alt="License: AGPLv3" src="https://shields.io/badge/License-AGPL%20v3-blue.svg">
</a>

一个主打便捷的 Docker 容器管理工具，提供服务端 API、Web 管理界面和移动端客户端。

## 功能

**容器**

- 一键更新容器，支持指定镜像与 tag
- 更新中断：镜像下载阶段可即时停止，容器保持原状
- 容器资源指标：CPU 占用、内存、网络速率（5s 实时刷新）
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
    image: liuloktar/dockercopilot:latest
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
| `DelOldContainer` | `true` | 更新容器后是否删除旧容器（旧容器会先以 `原名-时间戳` 备份，成功后删除） |

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
POST   /api/container/:id/update/cancel
GET    /api/container/updates
GET    /api/container/:id/rollback*
GET    /api/container/:id/logs?tail=&timestamps=

`GET /api/container/stats` 返回所有 running 容器的资源指标：
`cpuPercent`、`memoryUsage/Limit/Percent`、`netRx/netTx`、`netRxRate/netTxRate`、
`blockRead/blockWrite`、`pids`（v2.2.0+）。

`POST .../update/cancel` 仅在镜像拉取阶段可中断（容器未改动，直接停止下载）；
进入容器变更阶段后返回 400，更新会执行到底，失败时自动恢复旧容器。
`GET /api/container/updates` 返回最近 50 条更新记录（含进行中与历史，v2.3.0+）。
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
├── .env                     # 变量文件（可选）
├── .backups/                # 保存前自动备份，每文件最多保留 20 份
├── .trash/                  # 软删除的文件，可恢复
├── .history.jsonl           # 版本历史，最多 200 条
├── .queue/                  # 待执行请求（服务端写入）
└── .results/                # 执行结果（watcher 写入）
```

- 覆盖保存前会先备份旧内容，**备份失败则取消保存**
- 删除是软删除，进入 `.trash` 可从界面或接口恢复
- 回滚前会把当前内容再备份一次，避免回滚成为新的丢失点
- 文件名校验：允许单层 `.yml` / `.yaml` 与 `.env` 系列，拒绝路径穿越

### 环境变量

支持在 compose 中使用变量插值：

```yaml
services:
  web:
    image: nginx:${NGINX_TAG:-latest}
    environment:
      DB_PASSWORD: ${DB_PASSWORD}
```

变量取自 stacks 目录下的 `.env`：

```bash
# /data/stacks/.env
NGINX_TAG=1.27
DB_PASSWORD=change_me
```

- `.env` 和 `.env.production` 这类变体都能通过界面创建、编辑
- 保存或校验 compose 时会注入 `.env`，**未定义且无默认值的变量会直接报错**，
  不会等到执行时才失败
- 带默认值的写法 `${VAR:-default}` 始终允许

**注意**：校验使用服务端容器内 `/data/stacks/.env`，而实际执行由宿主机上的
`docker compose` 完成，它读取的是宿主机侧的 `.env` 与进程环境变量。
请确保两边一致，否则可能出现「校验通过但执行失败」。

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

## 开发环境

Go 1.23+。

### 本地构建

服务端通过 `//go:embed front/*` 把 Web UI 打进二进制，**编译前必须先准备好 `front/` 目录**，
否则界面会是空白。

使用脚本一步完成（前端 → front/ → 编译）：

```bash
# 前端仓库默认取同级的 Docker-Copilot-React
./scripts/build-local.sh

# 指定平台
./scripts/build-local.sh linux arm64

# 指定前端目录
FRONTEND_DIR=/path/to/Docker-Copilot-React ./scripts/build-local.sh
```

产物位于 `dist/linux/<arch>/dockerCopilot`。

也可以手动执行：

```bash
cd ../Docker-Copilot-React && npm ci && npm run build
rm -rf ../dockerCopilot/front && cp -R dist ../dockerCopilot/front
cd ../dockerCopilot
go build -o dockerCopilot .
```

### 运行测试

```bash
go test ./internal/...
```

## 发布

项目通过 GitHub Actions 自动构建多架构镜像并推送到 Docker Hub。

### 发布流程

**1. 更新版本号**

```bash
echo "v2.1.7" > version
echo "v2.1.7-FNOS" > fn_version
echo "v2.1.7-UGREEN" > ugreen_version
```

**2. 提交并推送**

```bash
git add version fn_version ugreen_version
git commit -m "chore: 版本号升至 v2.1.7"
git push fork latest
```

**3. 打 tag 触发发布**

```bash
git tag v2.1.7
git push fork v2.1.7
```

Workflow 会自动完成：构建前端 → 交叉编译 amd64/arm64 → 运行测试 →
Buildx 构建多架构镜像 → 推送。

产出：

```
{username}/dockercopilot:2.1.7
{username}/dockercopilot:latest
```

### 手动触发

GitHub 仓库 → **Actions → Publish Docker Image → Run workflow**：

- `version`：指定版本号，留空则读取 `version` 文件
- `push_latest`：是否额外打 `latest` 标签（推送 tag 时默认打）

### 构建配置

**Secrets（必需）**

| 名称 | 说明 |
| --- | --- |
| `DOCKERHUB_USERNAME` | Docker Hub 用户名 |
| `DOCKERHUB_TOKEN` | Docker Hub Access Token（Read & Write） |

**Variables（可选）**

| 名称 | 默认值 | 说明 |
| --- | --- | --- |
| `DOCKERHUB_REPOSITORY` | `dockercopilot` | 镜像名 |
| `FRONTEND_REPOSITORY` | `singleton-altman/Docker-Copilot-React` | 前端仓库 |
| `FRONTEND_REF` | `master` | 前端分支或 tag |

前端改动需要先推送到前端仓库的 `master`，再触发服务端发布，否则镜像里仍是旧页面。

## 安全说明

- 挂载 `docker.sock` 等价于宿主机 root 权限，**不要暴露到公网**
- compose 文件与执行请求都做文件名、动作白名单校验
- 关注仓库的请求地址固定为 `https://api.github.com`
- compose 执行的实际权限由宿主机 watcher 承担，服务端不执行任意命令

## 关于本 Fork

本项目是 [onlyLTY/dockerCopilot](https://github.com/onlyLTY/dockerCopilot)（AGPLv3）
的增强分支，在原功能之上新增：容器资源指标、更新中断、compose 变量文件、
发布监控增强，以及独立于上游的 fnOS / UGREEN 更新渠道。

遵循 AGPLv3 协议，欢迎 Issue 与 PR。
