# Backup Management Center 部署说明

## 文件布局
- `bin/backup-center-server`：控制平面，提供 HTTP API、Web UI、gRPC 通道与调度器。
- `bin/backup-center-agent`：运行于每台受管 Linux 主机，仅通过出站连接访问 Server。

## Server 要求
- Linux x86_64，使用 systemd。
- SQLite 数据库保存在 `BMC_DATA_DIR`。请离线备份此目录，其中包含数据库、Server 实例 ID，以及安装时提供的主密钥。

## 安装 Server
```sh
sudo useradd --system --home /var/lib/bmc --shell /usr/sbin/nologin bmc || true
sudo mkdir -p /var/lib/bmc /etc/bmc
# 创建 32 字节主密钥：
sudo sh -c 'head -c 32 /dev/urandom > /etc/bmc/master.key && chmod 600 /etc/bmc/master.key && chown bmc:bmc /etc/bmc/master.key'
# HTTPS 与 Agent gRPC 共用的 TLS 证书和私钥：
sudo cp server.crt server.key /etc/bmc/ && sudo chown bmc:bmc /etc/bmc/*
sudo cp deploy/systemd/bmc-server.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now bmc-server
```

环境变量可通过 systemd 的 `Environment=` 或 drop-in 配置：
- `BMC_LISTEN_ADDR`：HTTP 和 Web UI 监听地址，默认 `:8080`。
- `BMC_GRPC_ADDR`：Agent 控制通道监听地址，默认 `:9090`。
- `BMC_METRICS_ADDR`：Prometheus 指标监听地址，默认 `127.0.0.1:9100`，仅限本机访问。
- `BMC_DATA_DIR`：数据目录，默认 `./data`。
- `BMC_PUBLIC_URL`：外部访问地址。
- `BMC_MASTER_KEY_FILE`：生产环境必填，内容必须为 32 字节主密钥。
- `BMC_TLS_CERT_FILE` / `BMC_TLS_KEY_FILE`：生产环境必填。

首次启动后，访问 `https://<server>/`，在 `/setup` 创建唯一管理员。创建完成后，该接口会永久不可用。

## 安装 Agent
请先在目标主机安装 `restic`、`rclone`，以及需要备份的数据库客户端：`postgresql-client`、`mysql-client`、`mongodb-database-tools`、`sqlite3`。Agent 只进行探测，不会自行下载这些工具。

在 Web UI 中创建注册令牌（Agents → 生成注册令牌），然后执行：

```sh
sudo useradd --system --home /var/lib/bmc-agent --shell /usr/sbin/nologin bmc-agent || true
sudo mkdir -p /var/lib/bmc-agent
sudo cp deploy/systemd/bmc-agent.service /etc/systemd/system/
sudo systemctl edit bmc-agent   # 设置 BMC_SERVER_GRPC_URL 和一次性 BMC_ENROLLMENT_TOKEN
sudo systemctl enable --now bmc-agent
```

Agent 环境变量：
- `BMC_SERVER_GRPC_URL`：Server gRPC 端点的 `host:port`。
- `BMC_ENROLLMENT_TOKEN`：一次性令牌，仅首次启动需要。
- `BMC_AGENT_STATE_DIR`：默认 `/var/lib/bmc-agent`。
- `BMC_AGENT_DATA_DIR`：导出与恢复的临时工作目录，默认 `<state>/scratch`。容量至少应为最大逻辑导出体积的 1.2 倍。
- `BMC_SERVER_TLS=0` 和/或 `BMC_DEV_INSECURE=1`：仅用于本地开发。

Agent 身份保存在 `<state>/identity.json`，权限为 `0600`。若身份文件丢失，请在 UI 中吊销旧 Agent 后重新注册；系统不会自动重建身份。

unit 默认启用 `NoNewPrivileges=true` 和 `UMask=0077`。由于备份需要读取管理员选择的任意路径，默认不启用会阻断源路径的文件系统沙箱。请仅向服务用户授予待备份路径的最小读取权限。

## 版本与升级
Server 与 Agent 使用语义化版本握手：主版本不一致时拒绝连接；次版本不一致时允许连接，并在 UI 中提示告警。升级顺序固定为先升级 Server，再逐台滚动升级 Agent。
