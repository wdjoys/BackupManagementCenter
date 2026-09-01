# Backup Management Center 部署文档

本文档是仓库唯一权威部署说明。Server 新部署从两个完整 Compose 入口中二选一；已有部署继续使用兼容入口。

- `docker-compose.tls.yml`：Server 直接提供 TLS。
- `docker-compose.proxy.yml`：外部反向代理终止 TLS。
- `docker-compose.yml`：已有部署兼容入口，不改变原 Secret 和环境变量。
- `docker-compose.agent.yml`：每台受管主机运行一个 Agent。

不要提交真实 `.env`、注册令牌、主密钥、证书私钥或存储凭据。

## 1. 前置条件

需要 Docker Engine 24+ 和 Docker Compose v2。Server 提供 Web/API（8080）和 Agent gRPC（9090），Agent 只建立出站 gRPC 连接。

## 2. 新部署 Server

### Server 直接提供 TLS

准备证书并复制环境示例：

```sh
cp deploy/.env.tls.example deploy/.env.tls
# 修改 BMC_PUBLIC_URL、BMC_TLS_CERT_FILE、BMC_TLS_KEY_FILE
docker compose --env-file deploy/.env.tls -f deploy/docker-compose.tls.yml up -d --build
curl -fk https://backup.example.com/health/ready
```

模板默认发布宿主机 `443 -> 8080` 和 `9090 -> 9090`，只需要证书和私钥两个 Compose Secret。主密钥首次启动自动生成到 `bmc-data` 卷的 `/var/lib/bmc/master.key`。

### 外部反向代理终止 TLS

```sh
cp deploy/.env.proxy.example deploy/.env.proxy
# 修改 BMC_PUBLIC_URL
docker compose --env-file deploy/.env.proxy -f deploy/docker-compose.proxy.yml up -d --build
curl -fk https://backup.example.com/health/ready
```

模板固定 `BMC_TLS_MODE=none`，默认只绑定 `127.0.0.1:8080` 和 `127.0.0.1:9090`，不声明任何 Secret。可在 `.env.proxy` 中覆盖绑定地址和端口；代理必须同时转发 Web/API 与支持 HTTP/2 的 gRPC。

两种新部署都必须在首次启动后立即备份主密钥；丢失后数据库中的加密凭据无法恢复：

```sh
docker run --rm -v bmc-data:/var/lib/bmc -v "$PWD:/backup" alpine \\
  cp /var/lib/bmc/master.key /backup/master.key
```

## 3. 部署 Agent

从 Server UI 获取一次性 enrollment token：

```sh
cp deploy/.env.agent.example .env.agent
# 填写 BMC_SERVER_GRPC_URL、BMC_ENROLLMENT_TOKEN 和目录
docker compose --env-file .env.agent -f deploy/docker-compose.agent.yml up -d --build
```

推荐使用带 scheme 的地址：`https://backup.example.com:9090` 或明文 `http://host:9090`。旧格式 `host:port` 仍兼容，未设置 `BMC_SERVER_TLS` 时默认启用 TLS，显式 `0/1` 继续生效。注册完成后删除 enrollment token 并重建容器；不要共享 Agent 状态卷。

Compose 默认只读挂载宿主机 `/etc`、`/srv`，恢复目录挂载到 `/backup-restore`。高级路径映射、roots、缓存、scratch 和并发设置仍可通过环境变量覆盖。

## 4. 已有部署升级

继续使用原命令和原文件，不需要迁移：

```sh
docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d --build
```

兼容入口保留原 `bmc_master_key`、证书 Secret 和所有 `BMC_*` 变量，因此已有数据卷、加密凭据和 Agent 身份原地复用。

如果要切换到简化模板：先停止 Server，备份数据库和原主密钥；将原 32 字节密钥复制到同一 `bmc-data` 卷的 `/var/lib/bmc/master.key` 并设为 `0600`，确认后再启动新模板。不能让同一数据库配合新生成的密钥启动。不可逆变更发生后，回滚必须恢复数据库与主密钥备份，不支持直接降级。

## 5. systemd

`deploy/systemd/bmc-server.service` 默认使用 `/var/lib/bmc/master.key`，缺失时自动生成。已有外部密钥可通过 drop-in 设置 `BMC_MASTER_KEY_FILE`。直接 TLS 仍需设置证书和私钥；反向代理模式设置 `BMC_TLS_MODE=none`。

`bmc-agent.service` 推荐将 `BMC_SERVER_GRPC_URL` 写成带 scheme 的地址；旧地址格式与 `BMC_SERVER_TLS` 仍兼容。

## 6. 运维检查

升级顺序始终是先 Server、确认 `/health/ready` 返回 200，再逐台升级 Agent。备份 `bmc-data` 卷和主密钥；不要使用 `latest` 作为可重复回滚标签。停止 Compose 不会删除卷，`down -v` 会永久删除状态数据。
