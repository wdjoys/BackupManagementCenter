# Backup Management Center 部署文档

版本基线：`v0.1.0`。本文档覆盖全部官方部署路径，内容与仓库当前实现一致。

## 目录

1. [架构总览](#架构总览)
2. [部署方式选择](#部署方式选择)
3. [通用准备](#通用准备)
4. [方式一：Docker Compose 部署（推荐）](#方式一docker-compose-部署推荐)
   - [Server Compose](#server-compose)
   - [Agent Compose](#agent-compose)
5. [方式二：systemd 二进制部署](#方式二systemd-二进制部署)
   - [构建产物](#构建产物)
   - [Server 安装](#server-安装)
   - [Agent 安装](#agent-安装)
6. [首次初始化](#首次初始化)
7. [Agent 注册流程](#agent-注册流程)
8. [备份目标与 Restic 仓库](#备份目标与-restic-仓库)
9. [环境变量参考](#环境变量参考)
10. [健康检查与监控](#健康检查与监控)
11. [升级与回滚](#升级与回滚)
12. [故障排查](#故障排查)

## 架构总览

```text
┌────────────────────────┐
│ 浏览器                  │
            ▼
┌─────────────────────────────────────────────┐
│ BMC Server（单节点）                          │
│  :8080 HTTP API + Web UI（TLS）              │
│  :9090 Agent gRPC（TLS）                     │
│  127.0.0.1:9100 Prometheus 指标               │
│  SQLite WAL + 主密钥加密列                    │
└───────────▲─────────────────────────────────┘
            │ 出站 TLS gRPC（长连接）
┌───────────┴────────────┐
│ BMC Agent（每台主机一个）│
│  restic / rclone        │
│  pg_dump / mysqldump    │
│  mongodump / sqlite3    │
└───────────┬────────────┘
            │ 备份数据直写
            ▼
     网盘存储目标（rclone remote）
```

关键原则：

- 控制面不承载备份数据流；Agent 直接写入网盘。
- Server 与 Agent 之间只有一条出站 gRPC 长连接，Agent 不监听端口。
- 每个 Agent × 存储目标对应独立 Restic 仓库与独立密码。
- BMC（由 Server 调度 Agent）必须是每个 Restic 仓库的唯一写入方；运维必须禁止在 BMC 之外执行 `backup`、`forget`、`prune`、`tag`、`init` 等写操作，否则无法保证快照缓存的一致性。

## 部署方式选择

| 场景 | 推荐方式 | 说明 |
| --- | --- | --- |
| 生产服务器（独占边缘） | Docker Compose，自行提供 TLS 证书 | 支持每次握手动态加载，续期无需重启 |
| 80/443 已被 Caddy/Nginx 占用 | Caddy 共存模式 | BMC 全明文，TLS 由边缘代理负责，见 [`deploy/caddy.md`](caddy.md) |
| 已有 systemd 运维体系 | systemd 二进制部署 | 直接使用发行版 restic/rclone/数据库客户端 |
| 本地开发 | `BMC_DEV_INSECURE=1` | 仅本机测试，禁止生产使用 |

多种方式不要混用同一台主机的数据目录。

## 通用准备

无论哪种部署方式，都需要：

### 1. DNS

```
backup.example.com → Server 公网 IP
```

验证：

```sh
dig +short backup.example.com
```

### 2. 端口规划

| 公网端口 | 用途 | 是否必须公网开放 |
| --- | --- | --- |
| TCP 443 | Web UI / API（HTTPS） | 是 |
| TCP 9090 | Agent gRPC（TLS） | 是，至少对所有 Agent 开放 |

### 3. 主密钥

32 字节随机密钥，用于 AES-256-GCM 加密数据库中的敏感列：

```sh
mkdir -p secrets
head -c 32 /dev/urandom > secrets/master.key
chmod 600 secrets/master.key
```

**主密钥丢失 = 数据库中所有凭据无法解密。必须离线备份主密钥。**

### 4. 国内镜像（可选）

容器构建默认使用：

- Go modules：`https://goproxy.cn,direct`
- pnpm：`https://registry.npmmirror.com`
- Debian APT：`mirrors.aliyun.com`
- Restic/Rclone Release 下载：`gh-proxy.com` 代理

可按需通过构建参数覆盖（见 Agent 镜像章节）。

---

## 方式一：Docker Compose 部署（推荐）

前置要求：

- Docker Engine 24+
- Docker Compose v2（`docker compose` 命令）

文件清单：

```text
docker-compose.yml              # 默认入口，包含 Server
docker-compose.server.yml       # Server 服务定义（TLS 证书自行提供）
docker-compose.agent.yml        # Agent 模板（在每台受管主机使用）
Dockerfile.server
Dockerfile.agent
.dockerignore
```

### Server Compose

#### 第 1 步：准备目录

```sh
cd /mnt/sdb/docker-project/BackupManagementCenter
mkdir -p secrets
head -c 32 /dev/urandom > secrets/master.key
chmod 600 secrets/master.key
```

#### 第 2 步：准备 TLS 证书

将证书链与私钥放入 `secrets/`：

```text
secrets/server.crt   # fullchain 证书链
secrets/server.key   # 私钥
chmod 600 secrets/server.key
```

证书来源任选其一：

- 企业 CA / 商业证书；
- Let's Encrypt 等 ACME 证书：80 端口可用时用 HTTP-01，否则用 DNS-01（acme.sh 等 ACME 客户端的 DNS 模式均可），签发后把 `fullchain.pem` / `privkey.pem` 复制到上述路径；
- 内网环境可用自签证书（Agent 侧需信任 CA 或临时设置 `BMC_DEV_INSECURE=1`），见 [自签证书](#自签证书)。

要求：证书 SAN 必须覆盖 Agent 与浏览器访问的域名；Server 在每次 TLS 握手时动态重读该文件，**替换证书后无需重启**。

#### 第 3 步：启动 Server

```sh
export BMC_PUBLIC_URL=https://backup.example.com
export BMC_MASTER_KEY_FILE=./secrets/master.key
export BMC_TLS_CERT_FILE=./secrets/server.crt
export BMC_TLS_KEY_FILE=./secrets/server.key

docker compose -f docker-compose.server.yml up -d --build bmc-server
```

端口映射：

```text
公网 443 → 容器 8080（HTTPS Web/API）
公网 9090 → 容器 9090（Agent gRPC）
```

#### 数据卷

| Volume | 内容 | 可否删除 |
| --- | --- | --- |
| `bmc-data` | SQLite 数据库、实例 ID | **永不删除** |

### 自签证书

**内网/无公网域名** → 自签 + 手动分发：

```sh
openssl req -x509 -newkey rsa:2048 -nodes \
  -keyout secrets/server.key \
  -out secrets/server.crt \
  -days 825 \
  -subj "/CN=backup.internal" \
  -addext "subjectAltName=DNS:backup.internal,IP:10.0.0.5"
```

然后在每台 Agent 上信任该 CA，或临时设置 `BMC_DEV_INSECURE=1`（仅限隔离内网）。

**已有企业 CA 证书** → 直接放置：

```text
secrets/server.crt   # fullchain
secrets/server.key   # 私钥
```

并调整 Compose 中 `BMC_TLS_CERT_FILE` / `BMC_TLS_KEY_FILE` 指向挂载路径。

### Agent Compose

Agent 镜像内置：`restic 0.18.0`、`rclone 1.69.0`、MongoDB Database Tools 100.12.2、`sqlite3`、PostgreSQL 客户端和 MariaDB 客户端。

#### 第 1 步：获取注册令牌

在 Server Web UI：**Agents → 生成注册令牌**。令牌 15 分钟有效且只能用一次。

#### 第 2 步：编写 `.env.agent`

 BMC_SERVER_GRPC_URL=backup.example.com:9090
 BMC_SERVER_TLS=1
 BMC_ENROLLMENT_TOKEN=<粘贴一次性令牌>
 BMC_SOURCE_ETC=/etc
 BMC_SOURCE_SRV=/srv
 BMC_SOURCE_ROOTS=/backup-sources
 BMC_RESTORE_ROOT=/var/lib/bmc-restore
 BMC_RESTORE_ROOTS=/backup-restore
 BMC_AGENT_MAX_CONCURRENCY=2
 BMC_RESTIC_CACHE_DIR=/var/lib/bmc-agent/.cache/restic
```

#### 第 3 步：启动

```sh
docker compose -f docker-compose.agent.yml --env-file .env.agent up -d --build
```

#### 第 4 步：清理令牌

注册成功后立即从 env 文件中删除令牌：

```sh
sed -i '/^BMC_ENROLLMENT_TOKEN=/d' .env.agent
docker compose -f docker-compose.agent.yml up -d
```

#### 源路径映射规则

Agent 容器内的路径 ≠ 主机路径。当前模板挂载：

```text
主机 /etc → 容器 /backup-sources/etc（只读）
主机 /srv → 容器 /backup-sources/srv（只读）
```

因此计划中的源路径必须写容器路径：

```json
{"paths": ["/backup-sources/etc", "/backup-sources/srv/myapp"]}
```

新增其他源目录时，编辑 `docker-compose.agent.yml` 增加只读挂载：

```yaml
    volumes:
      - /var/lib/myapp:/backup-sources/var-lib-myapp:ro
```

例如备份宿主机 `/root/nginx` 时，必须增加精确挂载（`BMC_SOURCE_ROOTS`
只是容器内 allowlist，不会自动创建或映射宿主目录）：

```yaml
    volumes:
      - /root/nginx:/backup-sources/root/nginx:ro
```

计划使用 `/backup-sources/root/nginx`。Agent 以 UID 65532 非 root 用户运行；
如果宿主机 `/root` 或该目录是 `0700`，请只对需要的路径授权：

```sh
sudo setfacl -m u:65532:--x /root
sudo setfacl -R -m u:65532:rX /root/nginx
```

安全红线：

- 不要挂载 `/` 或 `/var/run/docker.sock`。
- 不要给 Agent 容器 `privileged: true`。
- 只读挂载足够——恢复走 staging 目录再落盘，不需要对源目录写入。
- `/var/lib/bmc-agent/scratch` 是独立持久卷，不再使用固定 10GB tmpfs；应按估算制品大小配置卷容量。
- `/backup-restore` 必须是独立读写挂载，且 `BMC_RESTORE_ROOTS` 只列出允许覆盖的容器内路径。

Server 端数据库恢复安全门由 `BMC_ENABLE_DATABASE_RESTORE=1` 显式开启；默认关闭，完成预恢复备份/回滚演练后才应设置。

---

## 方式二：systemd 二进制部署

适合已有成熟 systemd 运维体系、希望直接使用发行版软件包的环境。

### 构建产物

在任意构建机执行：

```sh
make build-linux
```

产出：

```text
bin/backup-center-server-linux-amd64
bin/backup-center-agent-linux-amd64
```

复制到目标主机 `/usr/local/bin/` 并赋予执行权限。

### Server 安装

```sh
sudo useradd --system --home /var/lib/bmc --shell /usr/sbin/nologin bmc || true
sudo mkdir -p /var/lib/bmc /etc/bmc

# 主密钥
sudo sh -c 'head -c 32 /dev/urandom > /etc/bmc/master.key && chmod 600 /etc/bmc/master.key && chown bmc:bmc /etc/bmc/master.key'

# TLS 证书（企业 CA 或 Let's Encrypt 等签发）
sudo cp fullchain.pem /etc/bmc/server.crt
sudo cp privkey.pem   /etc/bmc/server.key
sudo chown bmc:bmc /etc/bmc/*

sudo cp deploy/systemd/bmc-server.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now bmc-server
```

证书更新：Server 在每次 TLS 握手时动态重读 `/etc/bmc/server.crt` / `server.key`，替换文件后无需重启。若使用宿主机 ACME 续期工具，在其续期钩子中复制新文件到 `/etc/bmc/` 即可。

> Server 支持每次 TLS 握手重新读取证书文件；即使不发送信号，新握手也会拿到新证书。

### Agent 安装

```sh
# 安装依赖（Ubuntu/Debian 示例）
sudo apt update
sudo apt install -y restic rclone sqlite3 postgresql-client mariadb-client
# MongoDB 工具需按官方文档安装 mongodb-database-tools

sudo useradd --system --home /var/lib/bmc-agent --shell /usr/sbin/nologin bmc-agent || true
sudo mkdir -p /var/lib/bmc-agent && sudo chown bmc-agent:bmc-agent /var/lib/bmc-agent

sudo cp deploy/systemd/bmc-agent.service /etc/systemd/system/
sudo systemctl edit bmc-agent
```

drop-in 中填写：

```ini
[Service]
Environment=BMC_SERVER_GRPC_URL=backup.example.com:9090
Environment=BMC_SERVER_TLS=1
Environment=BMC_ENROLLMENT_TOKEN=<一次性令牌>
# 如需限制 Agent 可读范围：
# ReadOnlyPaths=/srv/myapp
```

启动：

```sh
sudo systemctl enable --now bmc-agent
```

注册完成后移除令牌：

```sh
sudo systemctl edit bmc-agent   # 删除 BMC_ENROLLMENT_TOKEN 行
sudo systemctl restart bmc-agent
```

---

## 首次初始化

任一部署方式完成后：

1. 浏览器访问 `https://backup.example.com/`。
2. 自动跳转到 `/setup`。
3. 创建唯一管理员（用户名 ≥3 字符，密码 ≥10 字符）。
4. `/setup` 随即永久关闭（返回 404）。
5. 使用管理员账号登录，进入 Dashboard。

## Agent 注册流程

1. Web UI：**Agents 页 → 生成注册令牌**（15 分钟有效，一次性）。
2. 在目标主机按上文配置 `BMC_ENROLLMENT_TOKEN` 并启动 Agent。
3. Agent 出站连接 Server gRPC，提交主机名/OS/架构/版本和随机生成的 32 字节 secret。
4. Server 消耗令牌、保存 secret 哈希、分配 UUID。
5. Agent 将身份写入 `<state>/identity.json`（0600），此后不再需要令牌。
6. UI 中 Agent 变为 online，能力探测结果（restic/rclone/各数据库客户端路径与版本）自动上报。

身份丢失的处理：在 UI 吊销旧 Agent → 在主机上清空 state 目录 → 生成新令牌重新注册。系统不会自动重建身份。

## 备份目标与 Restic 仓库

### 创建存储目标

Web UI：**Storage → 导入 rclone 配置**

1. 在任意一台在线 Agent 所在主机（或你自己的电脑）运行 `rclone config` 完成网盘授权；
2. 把生成的 `rclone.conf` 内容粘贴到 UI；
3. 选择要使用的 remote 名称；
4. 选择用于验证的在线 Agent；
5. 点击验证（Agent 会执行 `rclone listremotes` 和 `rclone lsd <remote>:`）；
6. 保存后配置以 AES-256-GCM 密文入库。

首期支持所有 rclone 支持的 remote 类型（Google Drive、OneDrive、Dropbox、WebDAV、S3 兼容、本地目录等）；不做供应商 OAuth 回调。

### 绑定仓库

Web UI：**Storage → Repositories → 绑定仓库**

选择 Agent 和存储目标后，Server 自动：

1. 计算仓库路径 `<remote>:<remote_path>/<instance_id>/<agent_id>`；
2. 生成 32 字节随机 Restic 密码（加密存储）；
3. 通过 Agent 执行 `restic snapshots` 探测：
   - exit 0 → 仓库已存在，直接接入；
   - exit 10 → 仓库不存在，自动 `restic init`；
   - exit 12 → 报错 `wrong_repository_password`；
   - exit 11 → 报错 `repository_locked`。

之后创建计划时选择该仓库即可。

## 环境变量参考

### Server

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `BMC_LISTEN_ADDR` | `:8080` | HTTP/Web UI 监听地址 |
| `BMC_GRPC_ADDR` | `:9090` | Agent gRPC 监听地址 |
| `BMC_METRICS_ADDR` | `127.0.0.1:9100` | Prometheus 指标，仅本机 |
| `BMC_TLS_MODE` | `auto` | `auto`=使用证书文件；`none`=明文运行（置于 TLS 反代之后，如 Caddy 共存模式） |
| `BMC_DATA_DIR` | `./data` | SQLite 与实例 ID 目录 |
| `BMC_PUBLIC_URL` | 空 | 外部访问 URL |
| `BMC_MASTER_KEY_FILE` | 空 | 32 字节主密钥文件，生产必填 |
| `BMC_TLS_CERT_FILE` | 空 | TLS 证书链，生产必填 |
| `BMC_TLS_KEY_FILE` | 空 | TLS 私钥，生产必填 |
| `BMC_DEV_INSECURE` | 空 | 设为 `1` 时跳过证书与主密钥校验，仅本地开发 |

> Telegram 失败通知不使用环境变量：在 Web 界面「设置」页配置 Bot Token 与 Chat ID（二者必须同时填写，清除即禁用），保存后立即生效，无需重启。配置 `BMC_PUBLIC_URL` 后，通知会附带 `/runs/{id}` 详情链接。

### Agent

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `BMC_SERVER_GRPC_URL` | 必填 | Server gRPC 地址 `host:port` |
| `BMC_SERVER_TLS` | `1` | 是否启用 TLS |
| `BMC_ENROLLMENT_TOKEN` | 空 | 一次性注册令牌，仅首启 |
| `BMC_AGENT_STATE_DIR` | `./agent-state` | 身份及持久状态根目录 |
| `BMC_AGENT_DATA_DIR` | `<state>/scratch` | 导出/恢复临时空间 |
| `BMC_RESTIC_CACHE_DIR` | `<state>/.cache/restic` | 持久化 Restic metadata cache；不得指向 scratch |
| `BMC_AGENT_PROBE_INTERVAL` | `600` | 能力探测间隔（秒） |
| `BMC_DEV_INSECURE` | 空 | `1` 时跳过 Server 证书校验 |

## 健康检查与监控

| 端点 | 说明 |
| --- | --- |
| `GET /health/live` | 进程存活，始终 200 |
| `GET /health/ready` | 数据库+迁移+调度器+gRPC 就绪，否则 503 |
| `GET /metrics`（`BMC_METRICS_ADDR`） | Prometheus 格式指标 |

指标列表：

- `bmc_runs_total{operation,status}`
- `bmc_run_duration_seconds`
- `bmc_agents_online`
- `bmc_dispatch_queue_depth`
- `bmc_repository_last_check_timestamp`
- `bmc_agent_grpc_reconnects_total`

## 升级与回滚

### 版本兼容规则

- 主版本不同：Agent 拒绝连接。
- 次版本不同：允许连接，UI 显示告警。
- 升级顺序固定：**先 Server，后逐台 Agent**。

### Docker Compose 升级

```sh
# 0. 备份（离线保存！）
docker run --rm -v bmc-data:/data -v $PWD/backup:/backup alpine \
  tar czf /backup/bmc-data-$(date +%F).tar.gz -C /data .
cp secrets/master.key /secure/offline/place/

# 1. 拉取新代码
git pull

# 2. 重建并滚动
docker compose -f docker-compose.server.yml build --pull
docker compose -f docker-compose.server.yml up -d bmc-server
curl -fk https://localhost/health/ready

# 3. 逐台升级 Agent
ssh agent-host "cd ... && docker compose -f docker-compose.agent.yml build --pull && docker compose -f docker-compose.agent.yml up -d"
```

### systemd 升级

```sh
# Server
sudo systemctl stop bmc-server
sudo cp bin/backup-center-server-linux-amd64 /usr/local/bin/backup-center-server
sudo systemctl start bmc-server

# Agent（逐台）
sudo systemctl stop bmc-agent
sudo cp bin/backup-center-agent-linux-amd64 /usr/local/bin/backup-center-agent
sudo systemctl start bmc-agent
```

### 回滚

- Server：换回旧二进制/镜像重启即可（SQLite 迁移向前兼容旧版本的读路径有限，跨多个 minor 回滚前先咨询迁移日志）。
- Agent：直接换回旧二进制。不要复用或拷贝 `identity.json` 来"克隆"Agent。

### 离线备份核对清单

| 内容 | 方式 | 频率 |
| --- | --- | --- |
| `secrets/master.key` | 手动复制到密码管理器/离线介质 | 创建后一次，变更时 |
| `bmc-data` volume / `BMC_DATA_DIR` | tar 或 SQLite backup | 每日 |
| `bmc-certs` volume | 可选（丢了重新签发即可） | 不必须 |
| Agent `identity.json` | 不备份；丢失就吊销重注册 | — |

## 故障排查

### Server 无法启动：`config: BMC_TLS_CERT_FILE and BMC_TLS_KEY_FILE are required`

生产模式必须提供证书。确认：

- 文件存在且可读（容器内路径 vs 主机路径）。
- 证书链完整（`fullchain.pem` 而不是 `cert.pem`）。

临时开发可用 `BMC_DEV_INSECURE=1`，但禁止生产使用。

### Agent 一直 offline

依次检查：

```sh
# 1. Agent 进程活着吗？
docker logs bmc-agent        # 或 journalctl -u bmc-agent

# 2. 能解析 Server 域名吗？
nslookup backup.example.com

# 3. 9090 通吗？
nc -zv backup.example.com 9090

# 4. 令牌过期了吗？（15 分钟）
#   重新生成令牌并重启 Agent。

# 5. 证书可信吗？
#   自签证书场景需在 Agent 侧安装 CA 或设置 BMC_DEV_INSECURE=1。
```

### 任务失败：`mkdir /var/lib/bmc-agent/scratch/bmc-run-*: permission denied`

Agent 临时目录（`BMC_AGENT_DATA_DIR`，默认 `<state>/scratch`）对运行用户不可写。按部署方式处理：

```sh
# systemd：目录属主必须是 bmc-agent（曾以 root 手动运行过 agent 的主机常见）
sudo chown -R bmc-agent:bmc-agent /var/lib/bmc-agent && sudo chmod 700 /var/lib/bmc-agent

# Docker Compose：scratch 是独立持久卷；旧部署重建 Agent 容器后，
# 若状态卷内旧文件属主不是 65532，执行一次：
docker run --rm -v bmc-agent-state:/data alpine chown -R 65532:65532 /data
```

### 备份任务失败：`insufficient_temp_space`

数据库导出的临时空间不足。解决：

- Compose：扩容 `bmc-agent-scratch` 卷；
- systemd：扩大 `BMC_AGENT_DATA_DIR` 所在分区；
- 或调小计划的 `estimated_dump_bytes`（如果高估了）。

需求公式：`free_space ≥ estimated_dump_bytes × 1.3`。

### 备份失败：`repository_locked`

另一个 restic 进程持有锁（通常是上次被强杀的任务残留）。处理：

```sh
# 在 Agent 主机上
restic unlock -r <repository_path> --insecure-no-password-file ...
```

或在 UI 取消卡住的运行后重试。Server 对每个 repository 串行派发，正常情况不会并发冲突。

### 备份失败：`wrong_repository_password`

Restic 密码不匹配。该密码由系统生成并加密存储，通常意味着：

- 有人在网盘侧手动改过仓库；
- 或者存储目标的 remote 配置变了。

如果确认要放弃旧仓库，删除 Repository 后重新绑定（会重新 init）。

### Web UI 打不开但 `/health/live` 正常

- 检查反向代理（如有）是否正确转发 WebSocket：`/ws/runs/{id}` 需要 Upgrade 头。
- 浏览器控制台查看是否有混合内容（HTTPS 页面请求 HTTP 资源）。

### 证书更新后未生效

Server 在每次新 TLS 握手时动态重读证书文件，正常无需重启。排查顺序：

```sh
# 容器内看到的文件是否已更新
docker compose exec bmc-server cat /run/secrets/bmc_tls_cert | openssl x509 -noout -dates
```

- 文件未更新 → 检查你的续期工具是否真的写入了挂载的源文件；
- 文件已更新但仍是旧证书 → 确认客户端没有复用旧的长连接（重建连接即可）。

---

## 附：快速核对清单

上线前逐项确认：

- [ ] DNS 已生效，`dig` 解析正确
- [ ] 公网 443/9090 放行，80 仅在续期窗口开放
- [ ] `master.key` 已离线备份（两份以上）
- [ ] TLS 证书覆盖访问域名，`openssl s_client` 验证通过
- [ ] Server `/health/ready` 返回 200
- [ ] 管理员已创建，`/setup` 返回 404
- [ ] 至少一台 Agent online 且能力探测完整
- [ ] 存储目标验证通过，仓库 status = ready
- [ ] 第一条测试计划手动跑通，快照可浏览
- [ ] 恢复 dry-run 通过，实际恢复文件 SHA-256 校验一致
- [ ] `restic check` 计划启用（每周自动）
- [ ] `bmc-data` 卷的每日备份任务已配置
