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
| `COMPOSE_BUILTIN_EXECUTOR` | `true` | 是否启用内置 compose 执行器；置 `false` 时仅依赖宿主机 watcher |
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
POST /api/compose/deploy    {"filename","action"}   action: deploy|up|down|restart|pull
GET  /api/compose/job?jobId=
GET  /api/compose/history
```

`deploy` 是复合动作，等价于「先拉取镜像再启动」，用于更新镜像后立即生效。
拉取阶段允许个别镜像失败（如只有 build 没有 image 的服务），最终成败以启动结果为准。

执行采用**投递式**设计：请求先写入队列，再由内置执行器（或宿主机侧 watcher）
消费执行，详见下方「Compose 执行」。

#### 执行状态与兼容响应

`JobView` 字段为 `id`、`filename`、`action`、`status`、`createdAt`，以及可选的
`startedAt`、`updatedAt`、`result`、`configSha256`。时间均为 RFC3339 字符串。
`status` 为 `queued` / `running` / `succeeded` / `failed` / `interrupted`。
`interrupted` 表示**结果不确定，需核验实际容器/镜像状态**，不是确认业务失败，不能自动重试变更。

投递时服务端读取 compose 文件内容并计算 SHA-256 指纹（hex 小写），随任务记录透出，
便于核对「实际部署的是哪一份内容」；文件不存在或不可读直接返回业务错误（`code!=200`），
不会生成注定失败的任务。

投递响应保持 `data.jobID`，新增可选的 `data.job`（投递已持久化后，附加读取失败不会把投递改报失败）：

```json
{"code":200,"msg":"success","data":{"jobID":"example-web","job":{"id":"example-web","filename":"web.yaml","action":"deploy","status":"queued","createdAt":"2026-10-25T12:00:00Z","configSha256":"<sha256 hex>"}}}
```

查询保持 `data.done` / `data.result`，新增 `data.status` 和已知任务的 `data.job`。
终态 `done:true` 且 `job.result` 与旧 `data.result` 一致；活动态 `done:false`，
旧 `data.result` 保留零值对象，但 `job.result` 省略。`status` 与 `job.status` 一致。

```json
{"code":200,"msg":"success","data":{"done":true,"status":"interrupted","result":{"id":"example-web","filename":"web.yaml","action":"deploy","success":false,"output":"服务端执行进程已中断，执行结果不确定；请核验实际容器和镜像状态，勿自动重试","finishedAt":"2026-10-25T12:05:00Z","status":"interrupted","createdAt":"2026-10-25T12:00:00Z","startedAt":"2026-10-25T12:00:01Z"},"job":{"id":"example-web","filename":"web.yaml","action":"deploy","status":"interrupted","createdAt":"2026-10-25T12:00:00Z","startedAt":"2026-10-25T12:00:01Z","updatedAt":"2026-10-25T12:05:00Z","result":{"id":"example-web","filename":"web.yaml","action":"deploy","success":false,"output":"服务端执行进程已中断，执行结果不确定；请核验实际容器和镜像状态，勿自动重试","finishedAt":"2026-10-25T12:05:00Z","status":"interrupted","createdAt":"2026-10-25T12:00:00Z","startedAt":"2026-10-25T12:00:01Z"}}}}
```

未知 ID 不伪造成功：`data.job` 省略，`status:not_found`，`done:false`。

终态 `result` 还可包含两个可选字段（旧 watcher / 旧任务数据缺省，缺省即视为「服务端未提供」）：

- `configSha256`：投递时文件内容的 SHA-256，与 `job.configSha256` 对应；
- `services`：`up`/`deploy`/`restart` 成功后按 compose 文件采集的服务运行状态
  `[{name,state,health}]`（`health` 仅容器配置了 healthcheck 时非空，如 `healthy/unhealthy/starting`；
  `down`/`pull` 不产出）。采集只读且失败降级：枚举失败或未注入 Docker 客户端时只追加
  一条 `stage=ps` 的 `warning` 步骤（全部 running 视为 `success`，存在 `exited` 或
  `unhealthy` 亦为 `warning`），不会推翻已成功的 up 结论。

```json
{"code":200,"msg":"success","data":{"done":true,"status":"succeeded","result":{"id":"example-web","filename":"web.yaml","action":"deploy","success":true,"output":"...","finishedAt":"2026-10-25T12:05:00Z","status":"succeeded","configSha256":"<sha256 hex>","services":[{"name":"web","state":"running","health":"healthy"},{"name":"db","state":"running"}]},"job":{"id":"example-web","filename":"web.yaml","action":"deploy","status":"succeeded","createdAt":"2026-10-25T12:00:00Z","configSha256":"<sha256 hex>"}}}
```

```json
{"code":200,"msg":"success","data":{"done":false,"status":"not_found","result":{"id":"","filename":"","action":"","success":false,"output":"","finishedAt":"0001-01-01T00:00:00Z"}}}
```

历史保持 `data.results` 为最近 20 条已结束结果，新增 `data.jobs` 为**全部活动任务 + 最近 20 条终态**，
合并后按 `updatedAt`（缺省用 `createdAt`）倒序，活动任务不受终态上限挤出。
空列表为 `[]`。例如 `{"code":200,"msg":"success","data":{"results":[],"jobs":[]}}`。
旧 watcher 无新增字段的结果仍按 `success` 推导成功/失败，时间优先使用仍存在的任务记录，
无创建时间时回退 `finishedAt`；未知来源 claim 仅显示 `running`，不猜测执行结果。

业务错误沿用响应 JSON 的 `code`（如提交/查询错误 `400`、历史读取错误 `500`）和 `msg`；
这些接口仍沿用 HTTP 200 包裹业务响应，调用端必须检查 `code`。
一次查询失败不改变任务生命周期。

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
├── .queue/                  # 待执行请求及已认领的 .json.claim
├── .jobs/                   # 内置执行器 owner、跨进程锁及 running 生命周期
└── .results/                # 最终执行结果（内置执行器或 watcher 写入）
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

**注意**：校验使用服务端容器内 `/data/stacks/.env`；实际执行时 compose 同样
在容器内读取该目录的 `.env` 与进程环境变量，两边保持一致。若改用宿主机
watcher 执行，则读取的是宿主机侧的 `.env`，请确保两边一致，
否则可能出现「校验通过但执行失败」。

## Compose 执行

服务端容器不直接调用宿主机的 compose 二进制，执行拆成两段：

```
客户端 → 服务端（写 .queue/{jobId}.json）
                  ↓
         内置执行器轮询（容器内 docker compose，经挂载的 docker.sock）
                  ↓
         docker compose -f ... pull && up -d
                  ↓
         写 .results/{jobId}.json
                  ↓
客户端 ← GET /api/compose/job?jobId=
```

**内置执行器（默认）**：镜像自带 docker CLI 与 compose 插件，通过挂载的
`docker.sock` 操作宿主机 Docker，普通安装（含 NAS）开箱即用，无需在宿主机
部署任何东西。可用 `COMPOSE_BUILTIN_EXECUTOR=false` 关闭。

内置执行器的行为：

- 接受 `deploy` / `up` / `down` / `restart` / `pull`
- `deploy` = `pull` + `up -d` 合成一次执行；
  pull 阶段带 `--ignore-pull-failures`，普通拉取失败不阻断启动，成败以 up 为准；
  但取消/超时中断不能跨过阶段边界继续启动，进入变更阶段后的中断不承诺回滚
- 提交与消费侧均校验单层 YAML 文件名与动作白名单；不能投递 `.env`
- 超过 5 分钟的请求视为过期，直接写失败结果，不执行
- 单条命令 10 分钟执行超时（`deploy` 的 pull 与 up 各自独立计时），中断/超时结果不确定
- 内置执行器结果保留最近 50 条，清理对应生命周期；活动任务不裁剪
- 不更改默认项目名，不使用 `--remove-orphans`；保留已有按文件归属识别的精确孤儿清理
- 请求、owner、生命周期和结果采用临时文件、同步、原子 rename；**最终结果成功落盘后才删除 claim**，
  写失败保留 claim 与生命周期，不能为重试写盘而再次执行变更
- 启动时取得 `.jobs/.executor.lock` 跨进程独占锁后，只做一次恢复：使用持久 instance + 进程 owner 标记，
  将明确属于本内置执行器旧进程的 running/claim 转为 `interrupted`，`result.success=false` 且提示结果不确定、需核验；
  已落盘最终结果优先，绝不自动重放。旧版无 owner 或外部来源不明 claim 不改动、不误判中断
- 该锁要求本地文件系统支持 `flock` 与目录同步；不保证 NFS/分布式存储上的多实例队列语义。
  owner/生命周期/内置结果为 `0600`；投递队列保持旧 `0644` 可读权限。
  外部 watcher 仍需对队列/结果目录具备原有读写权限

**剩余边界**：本批未加入幂等键、配置快照或自动重试。收到投递响应前若断网，任务可能已入队，
客户端不得自动重提 POST，应先查历史并核验。配置与 `.env` 在排队期间可能变化，任务执行当前文件，
不保证提交时快照。claim 认领后、owner 落盘前崩溃会留下来源不明 claim，保守保留供人工核验；
外部 watcher 不回写结果时可能持续显示 running。结果写盘失败期间也可能保持 running，
重启恢复后才确定为 interrupted；这不能当作确认业务失败或自动重试依据。
内置执行器关闭时不运行恢复；重新启用后才处理明确 owner 记录。

**外部 watcher（可选）**：宿主机侧 watcher 仍可消费同一队列，与内置执行器
靠文件原子认领共存，谁先认领谁执行，不会重复执行。设置
`COMPOSE_WATCH_DIR=/path/to/data/stacks` 后启动 watcher 即可。

**注意**：在容器内执行时，compose 文件里的**相对路径 bind 挂载**（如
`./config:/config`）会相对 `COMPOSE_DIR` 在容器内的位置解析。建议 compose
里使用宿主机绝对路径或具名卷，避免宿主机与容器路径视角不一致。

**注意**：内置执行器关闭且宿主机 watcher 未运行时，界面上的执行按钮只会
写入队列文件，不会真正执行，客户端会一直显示「等待宿主机执行」直到超时。

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
- compose 文件与执行请求都做文件名、动作白名单校验，消费侧还有二次校验
- 关注仓库的请求地址固定为 `https://api.github.com`
- compose 执行命令由固定白名单拼装，服务端不执行任意命令

## 关于本 Fork

本项目是 [onlyLTY/dockerCopilot](https://github.com/onlyLTY/dockerCopilot)（AGPLv3）
的增强分支，在原功能之上新增：容器资源指标、更新中断、compose 变量文件、
发布监控增强，以及独立于上游的 fnOS / UGREEN 更新渠道。

遵循 AGPLv3 协议，欢迎 Issue 与 PR。
