# Compose 部署说明

本项目支持两种部署方式：

- `docker-compose.server.yml`：部署控制面 Server。
- `docker-compose.agent.yml`：在每台被管理的 Linux 主机上单独部署一个 Agent。

不要把所有 Agent 与 Server 放在同一个 Compose 服务中。Agent 必须运行在实际需要读取备份源目录的主机上。

## 前置条件

- Docker Engine 24+。
- Docker Compose v2，命令为 `docker compose`。
- Server 主机可被 Agent 通过 TCP `9090` 访问。
- 生产环境准备 TLS 证书、私钥和 32 字节主密钥。
- Agent 镜像已包含 `restic`、`rclone`、MongoDB Database Tools、`sqlite3`、PostgreSQL 客户端和 MariaDB 客户端。

## Server Compose 部署

在仓库根目录创建密钥与证书目录。仓库不会自带证书文件，因此不能直接执行 `cp server.crt ...`。

### 方式一：生成本地或内网测试用自签证书

适用于开发、局域网和临时验证环境。需要安装 `openssl`：

```sh
mkdir -p secrets
head -c 32 /dev/urandom > secrets/master.key
chmod 600 secrets/master.key

openssl req -x509 -newkey rsa:2048 -nodes \
  -keyout secrets/server.key \
  -out secrets/server.crt \
  -days 825 \
  -subj "/CN=backup.example.com" \
  -addext "subjectAltName=DNS:backup.example.com,DNS:localhost,IP:127.0.0.1"
chmod 600 secrets/server.key
```

访问地址必须与证书中的域名或 IP 匹配。例如使用 `backup.example.com`：

```sh
echo "127.0.0.1 backup.example.com" | sudo tee -a /etc/hosts
```

### 方式二：使用正式 TLS 证书

如果已经从 CA 获得证书，则执行：

```sh
mkdir -p secrets
head -c 32 /dev/urandom > secrets/master.key
chmod 600 secrets/master.key
cp /path/to/server.crt secrets/server.crt
cp /path/to/server.key secrets/server.key
chmod 600 secrets/server.key
```

`server.crt` 必须包含访问域名对应的 SAN；`server.key` 必须与证书匹配。

设置外部访问地址并启动：

```sh
export BMC_PUBLIC_URL=https://backup.example.com
export BMC_MASTER_KEY_FILE=./secrets/master.key
export BMC_TLS_CERT_FILE=./secrets/server.crt
export BMC_TLS_KEY_FILE=./secrets/server.key

docker compose -f docker-compose.server.yml up -d --build
```

或者使用默认入口：

```sh
docker compose up -d --build
```

端口：

- `8080`：HTTP / Web UI。
- `9090`：Agent gRPC 控制通道。

首次启动后访问 `https://<server>:8080/`，在 `/setup` 创建唯一管理员。`/setup` 成功使用一次后永久关闭。

Server 状态保存在 Docker volume `bmc-data` 中。必须定期备份该 volume，至少包括：

- SQLite 数据库。
- Server instance ID。
- 计划、Agent 身份哈希、审计记录和运行记录。

生产环境不要设置 `BMC_DEV_INSECURE=1`，也不要把主密钥、TLS 私钥写进 Compose 文件或提交到 Git。

## Agent Compose 部署

每台目标 Linux 主机单独执行一次。先从 Server Web UI 创建一次性 enrollment token：

```text
Agents → 生成注册令牌
```

在目标主机创建 `.env.agent`：

```dotenv
BMC_SERVER_GRPC_URL=backup.example.com:9090
BMC_SERVER_TLS=1
BMC_ENROLLMENT_TOKEN=<一次性注册令牌>
BMC_SOURCE_ETC=/etc
BMC_SOURCE_SRV=/srv
BMC_SOURCE_ROOTS=/backup-sources
BMC_RESTORE_ROOTS=/backup-restore
BMC_SCRATCH_MIN_FREE_BYTES=0
```

启动 Agent：

```sh
docker compose -f docker-compose.agent.yml --env-file .env.agent up -d --build
```

Agent 首次启动会将身份保存到 `bmc-agent-state` volume。注册完成后可以从 `.env.agent` 删除 `BMC_ENROLLMENT_TOKEN`，避免令牌长期留在环境文件中：

```sh
sed -i '/^BMC_ENROLLMENT_TOKEN=/d' .env.agent
docker compose -f docker-compose.agent.yml up -d
```

Agent Compose 默认挂载：

```text
主机 /etc → 容器 /backup-sources/etc，只读
主机 /srv → 容器 /backup-sources/srv，只读
```

因此，计划中的源路径必须使用容器路径，例如：

```json
{"paths":["/backup-sources/etc","/backup-sources/srv"]}
```

不要直接挂载整个主机根目录，也不要挂载 `/var/run/docker.sock`。如果必须备份其他目录，显式增加只读挂载：

```dotenv
BMC_SOURCE_APP=/srv/myapp
```

并在 Compose 文件中增加：

```yaml
- ${BMC_SOURCE_APP}:/backup-sources/app:ro
```

然后计划使用 `/backup-sources/app`。

例如要备份宿主机 `/root/nginx`，不能只把 `BMC_SOURCE_ROOTS` 写成
`/backup-sources/root`；必须同时显式挂载目录，并在计划中使用挂载后的容器路径：

```dotenv
BMC_SOURCE_NGINX=/root/nginx
```

```yaml
- ${BMC_SOURCE_NGINX}:/backup-sources/root/nginx:ro
```

```json
{"paths":["/backup-sources/root/nginx"]}
```

Agent 容器固定以 UID 65532 的非 root 用户运行。若宿主目录是 `0700`（例如
`/root`），即使 Docker 已完成 bind mount，Agent 仍会收到 `permission denied`。
仅给实际需要的目录授予遍历/读取 ACL，不要把 Agent 改成 root：

```sh
sudo setfacl -m u:65532:--x /root
sudo setfacl -R -m u:65532:rX /root/nginx
```

## Agent 状态、临时空间与权限

- `bmc-agent-state` 保存 `identity.json`，不能删除或在多台 Agent 间共享。
- 临时导出和恢复使用 `/var/lib/bmc-agent/scratch` 独立持久卷（不再使用 10GB tmpfs），并由镜像内非 root 用户（UID/GID 65532）访问。生产环境必须设置 `BMC_SOURCE_ROOTS`、`BMC_RESTORE_ROOTS`，且两类挂载分别保持只读/读写隔离。
- `bmc-agent-scratch` 持久卷应按最大逻辑数据库导出大小预留至少 1.3 倍空间；空间不足时任务会失败并返回 `insufficient_temp_space`。
- 容器以非 root 用户运行，并启用 `no-new-privileges`。
- 只读源目录仍可能包含敏感数据，应限制 Docker 主机访问权限并保护宿主机 Docker 权限。

## 故障排查：Agent 报 name resolver error: produced zero addresses

按顺序检查：

1. `BMC_SERVER_GRPC_URL` 必须是纯 `host:port`，不带 `https://` 前缀。
2. **`${VAR:- 默认值}` 中冒号后不要留空格**——空格会成为值的一部分，导致域名解析失败。正确写法：`${BMC_SERVER_GRPC_URL:-backup.example.com:9090}`。
3. 域名必须能从 Agent 容器解析：`docker exec bmc-agent-1 getent hosts <域名>`。
4. 若走 Caddy 443 共存模式：确认 Caddyfile 已为该域名配置 `@grpc protocol grpc` 分流到 BMC 的 9090（h2c），且 Server 以 `BMC_TLS_MODE=none` 运行；否则改用直连 `:9090`。

## 故障排查：Server 报 master key permission denied

文件型 secret 在非 Swarm 模式下按宿主机文件的属主/权限原样挂载。容器以 uid 65532 运行，因此：

```sh
chown 65532:65532 secrets/master.key   # 或 chmod 644 secrets/master.key
docker compose -f docker-compose.server.yml up -d --force-recreate bmc-server
```

每次重新生成 `master.key` 后需重复设置属主。

## 升级与回滚

### 从 Docker Hub 更新已发布镜像

GitHub Actions 在 `main` 分支推送成功后，会发布以下镜像：

```text
<DOCKERHUB_USERNAME>/backup-management-center-server:latest
<DOCKERHUB_USERNAME>/backup-management-center-agent:latest
```

生产机只需要保存 Compose 文件、`.env` 和 `secrets/`，不需要保存源码或执行本地构建。首次部署或迁移到镜像部署时，可在 `.env` 中显式设置镜像地址：

```dotenv
BMC_SERVER_IMAGE=your-dockerhub-user/backup-management-center-server:latest
```

Agent 主机在 `docker-compose.agent.yml` 对应的环境文件中设置：

```dotenv
BMC_AGENT_IMAGE=your-dockerhub-user/backup-management-center-agent:latest
```

更新命令会先拉取远端镜像，再以拉取到的镜像重新创建服务；`--no-build` 防止 Compose 因保留的 `build` 配置而在生产机本地构建：

```sh
# Server 主机
docker compose pull
docker compose up -d --no-build --remove-orphans
curl -fk https://localhost/health/ready

# Agent 主机
docker compose -f docker-compose.agent.yml --env-file .env.agent pull
docker compose -f docker-compose.agent.yml --env-file .env.agent up -d --no-build --remove-orphans
```

### GitHub Actions 自动更新

如需在 `develop` 分支镜像推送成功后自动更新测试/开发 Server 主机，先配置 Actions Variable `DEPLOY_ENABLED=true`，再在 GitHub repository settings → Secrets and variables → Actions 中配置：

```text
DEPLOY_HOST       Server 主机地址
DEPLOY_PORT       SSH 端口，可选，默认 22
DEPLOY_USER       SSH 用户
DEPLOY_SSH_KEY    该用户的私钥
DEPLOY_PATH       Compose 文件所在绝对路径
```

工作流只在 `main` 或 `develop` 推送涉及对应镜像的文件时构建并推送该镜像；`develop` 分支上，只有至少一个镜像更新成功且 `DEPLOY_ENABLED=true` 时才执行 SSH 更新：`docker compose pull`，随后 `docker compose up -d --no-build --remove-orphans`，最后检查 `/health/ready`。未涉及镜像的提交不会触发构建、推送或部署。不要把私钥或 Docker Hub token 写入仓库文件；Actions Secrets 会被 GitHub 脱敏。

也可以在仓库根目录执行 Server 快捷命令：

```sh
make docker-update
```

`pull_policy: always` 会在正常 `docker compose up` 时检查远端镜像。生产升级仍应遵循“先 Server、确认 `/health/ready`、再逐台 Agent”的顺序，并在升级前备份 `bmc-data` volume 与主密钥。

### 回滚

将 `BMC_SERVER_IMAGE` 或 `BMC_AGENT_IMAGE` 改为 GitHub Actions 发布的 `sha-<短 SHA>` 标签，然后重新执行上面的 `pull` 与 `up --no-build` 命令。不要使用 `latest` 进行需要可重复性的回滚。

升级前备份 Server 数据 volume 和主密钥：

```sh
docker compose stop
# 另行备份 bmc-data volume 与 secrets/master.key
docker compose pull
docker compose up -d --no-build
```

升级顺序：

1. 先升级 Server。
2. 确认 `/health/ready` 正常。
3. 再逐台升级 Agent。

Server 与 Agent 使用版本握手：主版本不一致会拒绝连接，次版本不一致会允许连接但产生告警。不要通过复用 Agent 的 `identity.json` 来回滚或克隆 Agent。

## 常用运维命令

```sh
docker compose ps
docker compose logs -f bmc-server
docker compose exec bmc-server /usr/local/bin/backup-center-server --help
docker compose down
```

停止 Compose 不会删除 volume。只有确认已完成离线备份后，才使用：

```sh
docker compose down -v
```
