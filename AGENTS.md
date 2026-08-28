# Repository Guidelines

## 项目概览

Backup Management Center（BMC）是一个 Server/Agent 架构的备份管理系统：Server 提供控制面、HTTP API、Vue Web UI、调度器、SQLite 持久化和 Agent gRPC 控制通道；每台受管主机运行一个 Agent，执行文件系统或数据库备份、恢复、校验和快照操作。Agent 只主动向 Server 建立出站连接，Server 不主动拨入 Agent。

主要运行形态：

- `backup-center-server`：控制面，负责认证、计划、运行记录、存储目标、仓库、调度、审计、指标和 Web UI。
- `backup-center-agent`：受管主机上的执行面，首次使用注册身份，随后维持双向 gRPC stream 并调用本机 `restic`、`rclone` 及数据库客户端。
- `web/`：Vue 3 + TypeScript + Vite 前端；生产构建结果由 Go `embed.FS` 嵌入 Server。
- 部署和运维权威说明位于 `deploy/README.md` 与 `deploy/docker-compose.md`；仓库根目录没有独立 README。

## 架构与数据流

### Server 控制面

`cmd/server/main.go` 负责依赖装配和生命周期：读取 `BMC_*` 环境变量，创建数据目录和实例 ID，打开带 WAL 的 SQLite，执行迁移，配置 secret sealer、事件总线、指标、Agent registry、任务编排器、gRPC dispatcher、scheduler、HTTP API 和 gRPC/HTTP/metrics listeners。

一次计划或手动运行的大致路径：

```text
HTTP API / scheduler
  -> internal/server/jobs.Orchestrator
  -> internal/server/store（SQLite，持久化 Run）
  -> internal/dispatch.Dispatcher
  -> internal/server/dispatchgrpc（按 repository FIFO 排队）
  -> AgentControl.Connect gRPC stream
  -> internal/agent.Runner
  -> internal/agent/pipeline + backup adapters + restic/rclone/DB tools
  -> RunProgress / LogEntry / RunResult
  -> store + internal/server/events.Bus
  -> REST 查询或 /ws/runs/{runID} 日志订阅
```

运行状态按 `internal/model/model.go` 的状态机流转：`queued -> dispatched -> running -> succeeded|failed|cancelled`。`model` 包是 REST JSON、`ExecuteCommand.params_json` 和内部领域状态的共享来源；不要在其他包重复定义相同的状态或 payload。

### Agent 控制面协议

`api/proto/v1/agent.proto` 是唯一 Server/Agent wire protocol。Agent 首次通过 enrollment token 注册，之后发送 `Hello`、心跳和 capabilities，并接收 `ExecuteCommand`/`CancelCommand`。Agent 的 `Runner` 负责取消、进度、日志、终态结果和 command/run 幂等重放；`pipeline` 按 operation 选择 filesystem、PostgreSQL、MySQL、MongoDB、SQLite 等 adapter。Secret 只通过 proto 的 `SecretSet` 在受保护连接中传递，不放入 `params_json`。

### 调度与事件

- `internal/server/scheduler` 每 15 秒处理到期 cron slot、过期且 Agent 离线的 queued runs，以及 ready repository 的每周检查；通过窄接口 `RunStarter` 依赖 jobs，避免包循环。
- `internal/server/dispatchgrpc` 为每个 repository 保持 FIFO worker，保证同一 restic repository 的命令串行，并由 watchdog 处理超时。
- `internal/server/events` 是 gRPC 消息到 API/WebSocket 消费者之间的进程内 fan-out bus；慢订阅者会被丢弃，不阻塞生产者。
- `internal/server/api` 使用 chi；健康检查不认证，`/api/v1` 由 auth middleware 解析会话，setup/login 可在未认证状态访问，其他受保护路由对非 GET/HEAD 请求强制 CSRF；剩余路径交给 SPA handler。

### Web UI

`web/src/main.ts` 创建 Vue app、Pinia、vue-router 和 Element Plus。路由守卫先检查 setup 状态，再检查 `/auth/me`；`web/src/stores/auth.ts` 管理认证状态。`web/src/api/client.ts` 统一使用 `/api/v1`、cookie credentials 和非 GET 请求的 CSRF header；Vite 开发服务器将 `/api/v1` 和 `/ws` 代理到 `127.0.0.1:8080`。生产构建由 `internal/server/webui/embed.go` 嵌入。

## 关键目录

| 路径 | 用途 |
| --- | --- |
| `cmd/server/`、`cmd/agent/` | 两个可执行文件的入口和依赖装配。 |
| `internal/model/` | 共享领域模型、运行状态、操作类型和稳定错误码。 |
| `internal/server/api/` | HTTP 路由、REST handlers、SPA fallback 和 WebSocket 日志入口。 |
| `internal/server/jobs/` | 计划/系统运行、恢复、命令构建、等待和审计编排。 |
| `internal/server/store/` | `Store` 接口、SQLite 实现和嵌入式迁移。 |
| `internal/server/dispatchgrpc/`、`internal/server/agentreg/` | repository 队列、gRPC 调度和在线 Agent registry。 |
| `internal/server/scheduler/`、`events/`、`metrics/`、`auth/`、`config/` | 后台调度、事件 fan-out、Prometheus handler、会话/CSRF 和环境配置。 |
| `internal/agent/` | Agent 身份、连接重连、心跳、能力探测、命令 runner 和临时目录生命周期。 |
| `internal/agent/pipeline/`、`internal/agent/backup/`、`restic/`、`rclone/` | 执行流水线、备份类型 adapter、restic/rclone 命令封装。 |
| `internal/dispatch/`、`internal/secrets/`、`internal/version/` | 调度抽象、secret sealing 和构建版本。 |
| `api/proto/v1/` | 手写 proto 定义及生成的 Go gRPC/ protobuf 文件。 |
| `web/src/` | Vue views、layouts、Pinia stores、API types/client、router 和样式。 |
| `deploy/`、`Dockerfile.*`、`docker-compose*.yml` | systemd、Compose、镜像和生产部署说明。 |

## 开发命令

在仓库根目录执行 Make 命令；`web/` 是 pnpm workspace 的唯一 package。

```sh
# 生成 proto（需要 protoc、protoc-gen-go、protoc-gen-go-grpc）
make generate

# 构建前端并复制到 internal/server/webui/dist
make web-build

# 先构建 Web，再生成两个本机二进制到 bin/
make build

# Linux amd64 交叉编译两个二进制
make build-linux

# 运行全部 Go 测试
make test

# 整理 Go module（会修改 go.mod/go.sum）
make tidy

# 本地运行
make dev-server
make dev-agent

# Docker 镜像、Compose 启停
make docker-build
make docker-up
make docker-down

# 清理构建产物
make clean
```

前端单独开发：

```sh
cd web && pnpm install --frozen-lockfile
cd web && pnpm run dev       # Vite；API 和 WebSocket 代理到本机 Server
cd web && pnpm run build     # vue-tsc --noEmit && vite build
cd web && pnpm run preview
```

本地 Server 默认要求 TLS 证书、TLS 私钥和 32 字节 master key；仅本地开发可先设置 `BMC_DEV_INSECURE=1`。若同时运行不带 TLS 的 Agent，设置 `BMC_SERVER_TLS=0`，首次启动还需要一次性 `BMC_ENROLLMENT_TOKEN`。例如（POSIX shell）：

```sh
BMC_DEV_INSECURE=1 make dev-server
BMC_SERVER_TLS=0 BMC_ENROLLMENT_TOKEN=<token> make dev-agent
```

`Makefile` 的 Windows 分支要求 `C:/tools/git/usr/bin/sh.exe`，并使用 `cmd.exe /c pnpm`。当前没有可执行的 lint recipe：虽然 `.PHONY` 列出了 `lint`，但没有对应规则；`web/package.json` 也没有 test/lint script。不要把这些未配置目标当作可用命令。

## 代码约定与常见模式

### Go

- 使用窄接口和构造函数注入：例如 `store.Store`、`dispatch.Dispatcher`、`jobs.CommandSource`、scheduler 的 `RunStarter` 和 Agent 的 `ConfigProvider`。新增依赖优先扩展接口并在入口装配，而不是引入全局状态。
- 所有 I/O、数据库和后台任务传递 `context.Context`；后台 goroutine 必须有明确的 `Start`/`Stop` 或 context 退出路径。注意 `atomic.Bool`、channel、mutex 的生命周期和关闭顺序。
- 业务错误使用稳定的 `internal/model` error code；底层错误按现有风格用上下文包装（如 `fmt.Errorf("...: %w", err)`）。HTTP 错误保持 `{ "error": { "code": "...", "message": "..." } }` 形状。
- SQLite 写操作由 store 内部串行化，时间统一 UTC；迁移放在 `internal/server/store/migrations/`，由 `sqlite.go` 嵌入并按文件名顺序应用。
- 运行和安全边界不能绕过：storage target 使用 sealed config，数据库密码/会话 token 只保存哈希或加密形式，认证请求遵循 cookie + CSRF，生产 Server/Agent 使用 TLS。不要把 secret 写入日志、REST 响应或 `params_json`。
- 测试文件使用 `*_test.go`，测试函数使用 `Test...`；优先使用 `t.TempDir()`、fake store/dispatcher 和可注入依赖，避免测试依赖真实网络或宿主机工具。

### TypeScript / Vue

- 现有组件使用 `<script setup lang="ts">`、Composition API 和 2-space、无分号风格；页面按 `views/*View.vue` 或领域子目录组织。
- 全局状态使用 Pinia store（命名如 `useAuthStore`）；路由和 setup/auth 守卫集中在 `web/src/router/index.ts`；HTTP 调用通过 `web/src/api/client.ts`，不要在组件中复制 cookie、CSRF 或错误解析逻辑。
- 使用 `@/*` 指向 `web/src`；Vite 已配置 Element Plus auto-import/component resolver。`auto-imports.d.ts` 和 `components.d.ts` 是生成文件，修改依赖或组件后通过 Vite 构建重新生成，不要手工维护声明内容。
- `web/tsconfig.json` 开启 `strict`、`noUnusedLocals`、`noUnusedParameters` 和大小写一致性检查；新增代码应满足这些约束。
- 新增注释遵循仓库协议使用中文；变量名、函数名、API 路径和代码标识保持英文及现有命名风格。

## 配置与数据库平滑升级规则

每次发布或更新都必须保证已有部署可平滑升级，禁止要求用户手工重建配置或数据库。具体要求：

- **配置向后兼容**：新增配置项必须提供安全默认值；重命名或废弃配置项必须在至少一个兼容周期内继续识别旧名称，并记录清晰的迁移提示。不得因缺少新配置项导致已有实例无法启动。
- **配置迁移集中处理**：环境变量、配置文件和命令行参数的解析、默认值、别名及废弃项统一在 `internal/server/config/` 或 `internal/agent/config/` 处理；业务代码不得散落读取旧配置名称或自行解释默认值。
- **数据库只增量迁移**：数据库结构变更必须新增 `internal/server/store/migrations/` 下的有序 `.sql` 迁移文件，禁止修改或删除已执行的迁移文件，禁止通过重建数据库丢失现有数据。迁移必须保持事务性、可重复启动安全，并兼容当前版本写入的数据。
- **启动前安全升级**：Server 启动时自动执行数据库迁移；涉及数据重写、密文格式或不可逆变更时，必须先创建可恢复的 SQLite backup，并在备份或迁移失败时拒绝继续启动，不得以部分升级状态对外提供服务。
- **读写兼容窗口**：涉及字段改名、格式变更或配置迁移时，先实现兼容读取，再切换写入，确认旧数据已完成迁移后才能删除旧路径；跨 Server/Agent 或 API/proto 的变更必须先保证旧客户端仍可工作，再发布依赖新字段的客户端。
- **回滚边界明确**：每次升级都必须在发布说明或部署文档中说明迁移影响、备份位置、回滚步骤及是否支持降级。不可逆数据库迁移完成后不得声称支持直接降级，必须提供恢复备份的回滚方案。
- **升级测试必需**：新增或修改配置必须覆盖旧配置、默认配置和非法配置；新增或修改数据库迁移必须覆盖旧数据库升级到最新版本、重复执行迁移及失败回滚。改动 Server/Agent 协议时必须验证旧版本兼容路径。
- **变更记录**：涉及配置键、数据库 schema、密文格式、API/proto 契约的更新，必须同步更新 `deploy/README.md` 或对应权威部署文档，不能只修改代码。

平滑升级的验收标准：使用上一版本生成的配置和数据库启动新版本，服务完成迁移并通过 `/health/ready`；重复重启不产生二次变更；既有认证信息、计划、运行记录、存储目标、Repository 和 Agent 身份保持可用。

## 重要文件

- `Makefile`：proto、前端、Go、Docker、测试和本地开发命令；也是构建顺序的权威来源。
- `cmd/server/main.go`：Server 依赖图、监听器、恢复逻辑和优雅关闭。
- `cmd/agent/main.go`：Agent 配置、身份注册、runner/pipeline 装配和信号处理。
- `api/proto/v1/agent.proto`：唯一 Server/Agent 协议、版本握手和 command/result 消息。
- `internal/model/model.go`：JSON 领域模型、操作/状态枚举和稳定错误码。
- `internal/server/jobs/orchestrator.go`：运行创建、命令构建、恢复、等待、取消和 secret 解析。
- `internal/server/store/store.go`、`sqlite.go`、`migrations/0001_init.sql`：持久化接口、SQLite 行为和 schema。
- `internal/server/api/server.go`、`internal/server/auth/auth.go`：HTTP 路由、认证、会话和 CSRF 规则。
- `internal/agent/client.go`、`runner.go`、`pipeline/pipeline.go`：Agent stream、幂等执行和具体操作分发。
- `web/package.json`、`web/vite.config.ts`、`web/tsconfig.json`：前端脚本、代理/alias、类型检查和构建输出。
- `web/src/api/client.ts`、`web/src/router/index.ts`、`web/src/stores/auth.ts`：前端 API、导航守卫和认证状态。
- `Dockerfile.server`、`Dockerfile.agent`、`docker-compose.server.yml`、`docker-compose.agent.yml`：镜像运行时、端口、volume、secret 和主机挂载约定。
- `deploy/systemd/*.service`、`deploy/README.md`、`deploy/docker-compose.md`：Linux service hardening、生产环境变量、升级顺序和数据备份要求。

## 运行时与工具偏好

- Go module 声明为 Go `1.27`；Go 构建使用 `CGO_ENABLED=0`，SQLite 驱动为纯 Go 的 `modernc.org/sqlite`。
- 前端 package manager 是 pnpm，lockfile 为 `web/pnpm-lock.yaml`（lockfileVersion `9.0`）；workspace 只包含 `web`。`Dockerfile.server` 的前端构建阶段使用 Node `22`，本地 Node 版本未在 package manifest 中固定。
- 前端技术栈是 Vue 3、TypeScript、Vite、Pinia、vue-router 和 Element Plus；后端协议生成需要 `protoc` 与 Go protobuf/gRPC plugins。
- 生产 systemd 目标是 Linux x86_64；Compose 文档要求 Docker Engine 24+ 与 Docker Compose v2。Server 镜像以 distroless nonroot 运行，Agent 镜像带有 `restic`、`rclone`、SQLite、MariaDB 和 PostgreSQL 客户端；MongoDB Database Tools 需按目标环境额外提供。
- Server 默认监听 HTTP `:8080`、Agent gRPC `:9090`、仅本机 metrics `127.0.0.1:9100`。主要 Server 配置为 `BMC_LISTEN_ADDR`、`BMC_GRPC_ADDR`、`BMC_METRICS_ADDR`、`BMC_DATA_DIR`、`BMC_PUBLIC_URL`、`BMC_MASTER_KEY_FILE`、`BMC_TLS_CERT_FILE`、`BMC_TLS_KEY_FILE`、`BMC_DEV_INSECURE`；Agent 配置为 `BMC_SERVER_GRPC_URL`、`BMC_SERVER_TLS`、`BMC_ENROLLMENT_TOKEN`、`BMC_AGENT_STATE_DIR`、`BMC_AGENT_DATA_DIR`、`BMC_DEV_INSECURE`、`BMC_AGENT_PROBE_INTERVAL`。

## 测试与 QA

- 唯一统一测试命令是 `make test`，实际执行 `go test ./... -count=1`。
- 现有 Go 测试集中在：`internal/server/store/sqlite_test.go`（迁移和 CRUD）、`internal/server/jobs/jobs_test.go`（运行/命令/恢复）、`internal/server/scheduler/scheduler_test.go`（cron、超时和 weekly check）、`internal/server/auth/auth_test.go`（密码、CSRF、错误响应）、`internal/agent/*_test.go`（runner 幂等、执行、身份和工具探测）。测试使用标准库 `testing`，并大量使用临时目录和 fake 依赖。
- 没有发现前端测试脚本、覆盖率配置或仓库级 CI workflow；改动 Vue 时至少运行 `cd web && pnpm run build`，改动 Go 业务或协议时运行 `make test`，改动完整构建链时再运行 `make build`。
- 运行时 smoke check 可使用 `/health/live` 和 `/health/ready`；`/health/ready` 反映 Server 初始化完成状态。部署改动还应确认 Server 先升级、`/health/ready` 正常后再滚动升级 Agent。
