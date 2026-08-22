# Caddy 共存模式部署说明

适用场景：服务器公网 `80/443` 已被 Caddy 占用，BMC 不申请、不管理任何证书。

架构：

```text
浏览器 ── 443 ──> Caddy ──┬─ Web/API ──> http://127.0.0.1:8080
                          └─ gRPC ────> h2c://127.0.0.1:9090
Agent  ── 443（gRPC）──> Caddy
```

BMC 以 `BMC_TLS_MODE=none` 运行：HTTP 与 gRPC 均为明文，仅绑定 `127.0.0.1`；主密钥仍为必填。证书的申请与续期全部由 Caddy 负责。

## 第 1 步：启动 BMC（明文后端）

```sh
mkdir -p secrets
head -c 32 /dev/urandom > secrets/master.key
chmod 600 secrets/master.key

export BMC_PUBLIC_URL=https://backup.example.com
export BMC_MASTER_KEY_FILE=./secrets/master.key

docker compose -f docker-compose.server.caddy.yml up -d --build
```

BMC 只暴露：

```text
127.0.0.1:8080 → HTTP API / Web UI
127.0.0.1:9090 → gRPC（明文 h2c）
```

公网上不可直接访问。

## 第 2 步：配置 Caddy

### 情况一：Caddy 装在宿主机

`/etc/caddy/Caddyfile`：

```caddyfile
backup.example.com {
	# gRPC：按协议分流到明文 h2c 后端
	@grpc protocol grpc
	handle @grpc {
		reverse_proxy h2c://127.0.0.1:9090
	}
	# 其余全部走 Web UI
	handle {
		reverse_proxy http://127.0.0.1:8080
	}
}
```

重载：

```sh
systemctl reload caddy
```

### 情况二：Caddy 也是容器

让两者加入同一 Docker 网络，用服务名互访：

```yaml
# Caddy 的 compose 中
services:
  caddy:
    networks: [bmc]

networks:
  bmc:
    external: true
```

BMC 的 compose 增加：

```yaml
services:
  bmc-server:
    networks: [bmc]
    # 删除 ports 映射，改为仅网络内可达

networks:
  bmc:
    external: true
```

Caddyfile 上游改用容器名：

```caddyfile
backup.example.com {
	@grpc protocol grpc
	handle @grpc {
		reverse_proxy h2c://bmc-server:9090
	}
	handle {
		reverse_proxy http://bmc-server:8080
	}
}
```

## 第 3 步：验证

```sh
# Web 可达（HTTP 应 308 跳转 HTTPS）
curl -I http://backup.example.com/

# gRPC 通道 TLS 正常（应返回证书信息而非连接错误）
openssl s_client -connect backup.example.com:443 -servername backup.example.com < /dev/null 2>/dev/null | head -5

# BMC 健康检查（经 Caddy）
curl -s https://backup.example.com/health/live
```

## 第 4 步：Agent 配置

Agent 直接连 Caddy 的 443 端口，验证的是 Caddy 的公开证书：

```dotenv
BMC_SERVER_GRPC_URL=backup.example.com:443
BMC_SERVER_TLS=1
```

不要设置 `BMC_DEV_INSECURE=1`。

## 证书维护

零操作：

- 申请与续期由 Caddy 自动完成（它占用 80/443，ACME 天然可用）。
- BMC 不挂载任何证书文件。
- 续期时 Caddy 自动加载新证书，BMC 与 Agent 均无需重启。

唯一要求：Agent 使用的域名必须与 Caddy 站点域名一致。

## 安全说明

- 明文端口只绑定 `127.0.0.1`，请勿改成 `0.0.0.0`。
- 主密钥仍然必填并独立备份；`BMC_TLS_MODE=none` 只豁免证书，不豁免加密列密钥。
- 若 Caddy 与 BMC 不同主机，必须使用容器间私有网络或 WireGuard 等加密通道，禁止明文跨公网。

## 与 Certbot 模式的取舍

| | Caddy 共存模式 | Certbot 模式 |
| --- | --- | --- |
| 证书维护方 | Caddy | certbot 容器 |
| BMC 是否接触证书 | 否 | 是（只读挂载 + 动态加载） |
| Agent 连接路径 | 经 Caddy :443 | 直连 :9090 |
| 需要改动代码 | 已内置（`BMC_TLS_MODE=none`） | 已内置 |
| 适用 | 已有 Caddy 占用 80/443 | 无反代、BMC 独占边缘 |
