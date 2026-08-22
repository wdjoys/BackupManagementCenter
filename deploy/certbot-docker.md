# Certbot Docker 部署说明

本项目支持由 Certbot 容器管理 Server TLS 证书，不需要 Caddy 或 Nginx。

## 端口

- 公网 `80` → Server 容器 `8081`：Let’s Encrypt HTTP-01 challenge。
- 公网 `443` → Server 容器 `8080`：HTTPS Web UI / API。
- 公网 `9090` → Server 容器 `9090`：Agent gRPC TLS。

公网 DNS 必须满足：

```text
backup.example.com → Server 公网 IP
```

公网 TCP `80`、`443`、`9090` 必须按需放行。

## 首次申请证书

首次申请时 Server 还没有正式证书，因此先用 Certbot standalone 临时占用公网 `80`，不要先启动 `bmc-server`。

创建主密钥：

```sh
mkdir -p secrets
head -c 32 /dev/urandom > secrets/master.key
chmod 600 secrets/master.key
export BMC_DOMAIN=backup.example.com
export BMC_PUBLIC_URL=https://backup.example.com
export BMC_MASTER_KEY_FILE=./secrets/master.key
```

申请证书：

```sh
docker compose -f docker-compose.server.yml --profile certbot-init run \
  --rm \
  --service-ports \
  certbot-init certonly \
  --standalone \
  -d "$BMC_DOMAIN" \
  --email admin@example.com \
  --agree-tos \
  --no-eff-email
```

证书写入共享 volume `bmc-certs`。证书文件路径为：

```text
/etc/letsencrypt/live/backup.example.com/fullchain.pem
/etc/letsencrypt/live/backup.example.com/privkey.pem
```

申请成功后启动 Server：

```sh
docker compose -f docker-compose.server.yml up -d bmc-server
```

## 自动续期

Server 自己提供 ACME HTTP-01 webroot：

```text
Server 内部：:8081
公网映射：80 → 8081
Webroot：/var/lib/bmc/acme-webroot
```

启动 Certbot 续期容器：

```sh
docker compose -f docker-compose.server.yml --profile certbot up -d certbot
```

Certbot 容器每 12 小时执行：

```sh
certbot renew --webroot -w /var/lib/letsencrypt --quiet
```

Server 与 Certbot 共享：

```text
bmc-certs
bmc-acme-webroot
```

Server 在每次新的 TLS 握手时动态读取：

```text
/etc/letsencrypt/live/<域名>/fullchain.pem
/etc/letsencrypt/live/<域名>/privkey.pem
```

证书续期后不需要重启 Server。

## 续期测试

```sh
docker compose -f docker-compose.server.yml --profile certbot run --rm certbot renew --dry-run
```

## Server 环境变量

```dotenv
BMC_DOMAIN=backup.example.com
BMC_PUBLIC_URL=https://backup.example.com
BMC_MASTER_KEY_FILE=./secrets/master.key
```

Compose 会自动设置：

```text
BMC_ACME_ADDR=:8081
BMC_ACME_WEBROOT=/var/lib/bmc/acme-webroot
BMC_TLS_CERT_FILE=/etc/letsencrypt/live/<域名>/fullchain.pem
BMC_TLS_KEY_FILE=/etc/letsencrypt/live/<域名>/privkey.pem
```

## Agent 配置

Agent 不需要单独挂载证书。它通过 Server 域名验证 Server 证书：

```dotenv
BMC_SERVER_GRPC_URL=backup.example.com:9090
BMC_SERVER_TLS=1
```

生产环境不要设置：

```dotenv
BMC_DEV_INSECURE=1
```

## 国内镜像

默认镜像配置：

- Go：`https://goproxy.cn,direct`
- pnpm：`https://registry.npmmirror.com`
- Debian APT：`mirrors.aliyun.com`
- Restic / Rclone Release：`gh-proxy.com`

可通过 Docker 构建参数覆盖 Restic / Rclone 下载地址：

```sh
docker build \
  --build-arg RESTIC_BASE_URL=https://your-mirror/restic/releases/download \
  --build-arg RCLONE_BASE_URL=https://your-mirror/rclone/releases/download \
  -f Dockerfile.agent \
  -t backup-management-center-agent:latest .
```

## 升级

```sh
docker compose -f docker-compose.server.yml build --pull
docker compose -f docker-compose.server.yml up -d bmc-server
docker compose -f docker-compose.server.yml --profile certbot up -d certbot
```

不要删除以下 volume：

```text
bmc-data
bmc-certs
bmc-acme-webroot
```

主密钥不由 Certbot 管理，必须独立备份：

```text
secrets/master.key
```
