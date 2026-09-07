# Backup Management Center 部署文档

本文档是仓库唯一权威部署说明。默认推荐架构由外部反向代理（如 Nginx、Caddy、Traefik）终止公网 TLS，Server Compose 仅在宿主机本地监听 Web/API（`127.0.0.1:8080`）与 gRPC（`127.0.0.1:9090`）。反向代理必须同时转发 Web/API 与支持 HTTP/2 的 gRPC 流量。

文件入口概览：

- `deploy/docker-compose.yml`：推荐的 Server 部署模板（无 TLS Secret，通过数据卷自动生成主密钥）。
- `deploy/docker-compose.agent.yml`：受管主机 Agent 部署模板。
- `deploy/docker-compose.legacy.yml`：已有部署兼容模板（支持直接 TLS、Compose Secret 注入主密钥与证书）。

> **安全提示**：请勿将真实 `.env`、注册令牌（Token）、主密钥、私钥或存储凭据提交至 Git 仓库。

## 1. 快速部署 Server

准备环境并启动：

```sh
cp deploy/.env.example deploy/.env
# 编辑 deploy/.env，修改必填项 BMC_PUBLIC_URL（例如 https://backup.example.com）
docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d --build
curl --fail https://backup.example.com/health/ready
```

主密钥首次启动时自动生成并保存于 `bmc-data` 命名卷中的 `/var/lib/bmc/master.key`（权限 `0600`）。**首次启动后必须立即备份主密钥**；若主密钥丢失，数据库中所有已加密的存储目标与凭据均无法恢复：

```sh
docker run --rm -v bmc-data:/var/lib/bmc -v "$PWD:/backup" alpine \
  cp /var/lib/bmc/master.key /backup/master.key
```

## 2. 部署 Agent

在受管主机上部署 Agent：

```sh
cp deploy/.env.agent.example deploy/.env.agent
# 编辑 deploy/.env.agent：
# 1. 设置 BMC_SERVER_GRPC_URL（推荐带 scheme，例如 https://backup.example.com:9090 或 http://host:9090）
# 2. 填写从 Server Web UI 获取的一次性 BMC_ENROLLMENT_TOKEN
# 3. 按需调整 BMC_SOURCE_ETC、BMC_SOURCE_SRV 及 BMC_RESTORE_ROOT 宿主机挂载路径
docker compose --env-file deploy/.env.agent -f deploy/docker-compose.agent.yml up -d --build
```

**注意事项**：
- Agent 首次注册成功后，建议清空 `.env.agent` 中的 `BMC_ENROLLMENT_TOKEN` 并重建容器（`docker compose --env-file deploy/.env.agent -f deploy/docker-compose.agent.yml up -d`）。
- 备份源目录（`/etc`、`/srv`）以只读方式（`:ro`）挂载；恢复目标目录（默认 `/var/lib/bmc-restore`）以读写方式挂载。
- `bmc-agent-state` 卷用于持久化 Agent 身份，不可多主机共享。
- **重新安装接管（Takeover）**：若 Agent 状态卷丢失或需更换新机器接管原 Agent 数据，请在 Server Web UI 的 Agent 列表（离线状态）点击“重新安装接管”生成专用令牌，并在 `.env.agent` 中设置 `BMC_TARGET_AGENT_ID=<原AgentID>` 与 `BMC_ENROLLMENT_TOKEN=<接管令牌>`。启动后服务端会自动复用原 ID、保留全部仓库与计划并轮换密钥，接管成功后同样建议清空这两个变量。
## 3. 旧部署迁移

对于使用旧版 `docker-compose.yml`（直接 TLS 或 Secret 注入主密钥）的已有部署：

1. **备份数据**：先停止当前容器，完整备份 SQLite 数据库文件及原主密钥（`master.key`）和证书。
2. **继续以兼容方式运行**：将原有 `.env` 重命名或复制为 `deploy/.env.legacy`，使用兼容模板启动：
   ```sh
   docker compose --env-file deploy/.env.legacy -f deploy/docker-compose.legacy.yml up -d --build
   ```
   兼容模板完全保留了原 `bmc_master_key`、`bmc_tls_cert`、`bmc_tls_key` Secret 与全部环境变量。**严禁让同一数据库配合新生成的主密钥启动**。
3. **平滑切换至新推荐入口（反向代理模式）**：
   - 必须先将原有 32 字节主密钥复制到 `bmc-data` 卷的 `/var/lib/bmc/master.key` 并设置权限为 `0600`：
     ```sh
     docker run --rm -v bmc-data:/var/lib/bmc -v /path/to/old/master.key:/old_key:ro alpine \
       sh -c "cp /old_key /var/lib/bmc/master.key && chmod 600 /var/lib/bmc/master.key"
     ```
   - 确认主密钥文件已就绪后，再配置反向代理并使用 `deploy/docker-compose.yml` 启动。
   - 若迁移或不可逆变更失败，必须恢复原数据库与主密钥备份，不支持直接降级。

## 4. systemd

如需在主机直接作为 systemd 服务运行，可参考 `deploy/systemd/`：
- `deploy/systemd/bmc-server.service`：默认读取 `/var/lib/bmc/master.key`（缺失时自动生成）；反向代理模式下配置环境变量 `BMC_TLS_MODE=none`。
- `deploy/systemd/bmc-agent.service`：推荐 `BMC_SERVER_GRPC_URL` 使用带 scheme 的格式（如 `https://...` 或 `http://...`）。

## 5. 运维与升级

- **升级顺序**：始终先升级 Server，通过 `curl --fail <PUBLIC_URL>/health/ready` 确认返回 HTTP 200 后，再滚动升级各受管主机上的 Agent。
- **数据保留**：日常维护使用 `docker compose down` 停止容器，数据卷不会丢失；**严禁使用 `down -v`**，否则会永久销毁数据库及生成的本地主密钥。

## 6. 管理员密码重置

如果忘记 Server 管理员登录密码，可通过 `backup-center-server reset-admin` 命令清除管理员账号及活跃会话，重新触发 Web 引导初始化流程（此操作不会删除存储目标、备份计划、仓库及运行记录）：

- **Docker Compose 环境**：
  ```sh
  # 停止运行中的 Server 容器
  docker compose --env-file deploy/.env -f deploy/docker-compose.yml stop server

  # 使用 reset-admin 命令清除管理员信息并重新开放 Web 引导
  docker compose --env-file deploy/.env -f deploy/docker-compose.yml run --rm server reset-admin

  # 重新启动 Server
  docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d server
  ```
- **本地二进制或 systemd 环境**：
  ```sh
  # 停止服务后执行
  BMC_DATA_DIR=/var/lib/bmc backup-center-server reset-admin
  # 启动服务后访问 Web 控制台即可重新进行初始化设置
  ```
