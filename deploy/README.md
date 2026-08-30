# Backup Management Center 部署文档

本文档是仓库唯一权威部署说明。Compose 文件统一位于 `deploy/`：

- `deploy/docker-compose.yml`：Server（控制面），每台 Server 只运行这一份。
- `deploy/docker-compose.agent.yml`：Agent 模板，每台需要读取备份源的 Linux 主机单独运行一份。
- `deploy/.env.example`：Server 环境变量示例，可复制为 `deploy/.env` 后按实际环境修改。
- `deploy/.env.agent.example`：Agent 环境变量示例，可复制到 Agent 主机并命名为 `.env.agent`。

不要把 Server 与所有 Agent 合并到同一个 Compose 项目，也不要提交真实 `.env`、`.env.agent`、注册令牌、主密钥、TLS 私钥或其他凭据。

## 1. 前置条件与架构

需要 Docker Engine 24+ 和 Docker Compose v2（命令为 `docker compose`）。Server 提供 Web/API（容器端口 8080）和 Agent gRPC（容器端口 9090）；Agent 不监听入站端口，通过出站 gRPC 连接 Server，并直接把备份写入存储目标。

生产环境建议使用正式 TLS 证书。若 80/443 已由 Caddy 占用，可让 Server 使用 `BMC_TLS_MODE=none` 运行明文后端，仅绑定本机回环地址，由 Caddy 终止 TLS 并将 gRPC 以 h2c 转发到 9090。

## 2. 准备 Server

```sh
cd /path/to/BackupManagementCenter
mkdir -p deploy/secrets
head -c 32 /dev/urandom > deploy/secrets/master.key
chmod 600 deploy/secrets/master.key
cp deploy/.env.example deploy/.env
```

编辑 `deploy/.env`。主密钥必须离线备份；主密钥丢失后，数据库中的加密凭据无法恢复。正式 TLS 模式还需将证书链和私钥放入 `deploy/secrets/`，文件名默认分别为 `server.crt`、`server.key`。

### 自行提供 TLS 证书（默认）

将证书 SAN 覆盖浏览器和 Agent 使用的域名，然后启动：

```sh
docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d --build
curl -fk https://backup.example.com/health/ready
```

默认映射为宿主机 `443 → 8080` 和 `9090 → 9090`。首次启动后访问 `https://<域名>/setup` 创建唯一管理员；初始化成功后 `/setup` 永久关闭。

测试环境可用自签证书，但必须让 Agent 信任对应 CA；`BMC_DEV_INSECURE=1` 仅适用于隔离的本地测试，禁止生产使用。

### Caddy 共存 / 非 TLS 后端

当宿主机已有 Caddy 占用 80/443 时，编辑 `deploy/.env`：

```dotenv
BMC_TLS_MODE=none
BMC_PUBLIC_URL=https://backup.example.com
```

启动后，Server 应仅暴露 `127.0.0.1:8080` 和 `127.0.0.1:9090`。在 `deploy/.env` 中设置 `BMC_SERVER_BIND=127.0.0.1`、`BMC_SERVER_PORT=8080`、`BMC_GRPC_BIND=127.0.0.1`、`BMC_GRPC_PORT=9090`，不要将明文端口暴露到公网。

若使用 Caddy 且未准备证书文件，还应将 Compose secret 路径设为 `/dev/null`（Compose 仍会解析 secret 文件）：

```dotenv
BMC_TLS_CERT_FILE=/dev/null
BMC_TLS_KEY_FILE=/dev/null
```

Caddyfile 必须同时分流 Web 和 gRPC：

```caddyfile
backup.example.com {
    @grpc protocol grpc
    handle @grpc {
        reverse_proxy h2c://127.0.0.1:9090
    }
    handle {
        reverse_proxy http://127.0.0.1:8080
    }
}
```

Caddy 容器部署时，将 BMC 与 Caddy 加入同一外部 Docker 网络，并将上游改为 `h2c://bmc-server:9090` 与 `http://bmc-server:8080`。Agent 此时连接 `backup.example.com:443` 并设置 `BMC_SERVER_TLS=1`。Caddy 负责证书申请和续期；Server 仍必须配置并保护主密钥。

## 3. 部署 Agent

在 Server Web UI 的 **Agents → 生成注册令牌** 获取一次性令牌（有效期和使用次数以 UI 提示为准）。将 `deploy/.env.agent.example` 复制到目标主机，例如：

```sh
cp deploy/.env.agent.example .env.agent
```

填写 Server 的纯 `host:port` 地址（不要写 `https://`），注册令牌、源目录、恢复目录和路径映射。启动：

```sh
docker compose --env-file .env.agent -f deploy/docker-compose.agent.yml up -d --build
```

每台 Agent 必须在实际拥有备份源目录的主机上运行。默认显式只读挂载宿主机 `/etc`、`/srv`，映射为容器 `/backup-sources/etc`、`/backup-sources/srv`；恢复目录单独读写挂载。新增源目录时，必须同时增加 Compose 的只读挂载和 `BMC_SOURCE_PATH_MAPPINGS` 映射，禁止挂载整个主机根目录或 `/var/run/docker.sock`。

注册完成后立即从 `.env.agent` 删除 `BMC_ENROLLMENT_TOKEN`，再重新创建容器；身份保存在 `bmc-agent-state` 卷，不能删除、复制或在多台 Agent 间共享。

## 4. 环境变量与敏感信息

所有可填写变量均在 `deploy/.env.example` 和 `deploy/.env.agent.example` 中逐项中文注释。示例文件可直接复制，但默认域名、镜像、目录和并发数必须按环境审核。敏感项（`BMC_ENROLLMENT_TOKEN`、主密钥、TLS 私钥、任何存储凭据）只放在宿主机受限文件或 Secret 管理系统中，不得提交 Git、写入 Compose YAML 或粘贴到工单。

Compose 的 build context 已从 `deploy/` 正确指向仓库根目录；生产机若使用已发布镜像，设置 `BMC_SERVER_IMAGE` / `BMC_AGENT_IMAGE` 后使用 `--no-build`，无需保存源码。

## 5. 健康检查与运维

Server 更新后必须先确认就绪，再升级 Agent：

```sh
# Server
curl -fk https://backup.example.com/health/live
curl -fk https://backup.example.com/health/ready
docker compose --env-file deploy/.env -f deploy/docker-compose.yml ps
docker compose --env-file deploy/.env -f deploy/docker-compose.yml logs -f bmc-server

# Agent
docker compose --env-file .env.agent -f deploy/docker-compose.agent.yml ps
docker compose --env-file .env.agent -f deploy/docker-compose.agent.yml logs -f bmc-agent
```

健康检查失败时先查看日志、DNS、端口和证书。`BMC_SERVER_GRPC_URL` 必须是 `host:port`；经 Caddy 连接时必须存在 `@grpc protocol grpc` 的 h2c 分流。主密钥权限错误时，确保容器用户可读宿主机 Secret（通常将文件属主设为 UID 65532，或按主机安全策略调整权限）。

## 6. 升级与回滚

生产环境升级前备份 `bmc-data` 卷和 `deploy/secrets/master.key`。使用发布镜像时先拉取，再禁止本地构建：

```sh
# 先升级 Server
docker compose --env-file deploy/.env -f deploy/docker-compose.yml pull
docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d --no-build --remove-orphans
curl -fk https://backup.example.com/health/ready

# 再逐台升级 Agent
docker compose --env-file .env.agent -f deploy/docker-compose.agent.yml pull
docker compose --env-file .env.agent -f deploy/docker-compose.agent.yml up -d --no-build --remove-orphans
```

回滚时将 `BMC_SERVER_IMAGE` 或 `BMC_AGENT_IMAGE` 改为 CI 发布的不可变 `sha-<短 SHA>` 标签，然后重复 `pull` 和 `up -d --no-build`；不要用 `latest` 做可重复回滚。Server 与 Agent 应保持兼容版本，切勿通过复制 Agent 的 `identity.json` 克隆身份。

停止 Compose 不会删除卷。确认已有离线备份后才执行 `docker compose ... down -v`，否则会永久删除状态数据。
