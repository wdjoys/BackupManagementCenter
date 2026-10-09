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
docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d
curl --fail https://backup.example.com/health/ready
```

> **数据目录权限**：Server 容器以非 root 用户（uid/gid `65532`）运行，`/var/lib/bmc` 必须能被该用户写入，二选一：
> - **用模板默认的命名卷**（`bmc-data:/var/lib/bmc`）：**无需任何 chown**。镜像已把 `/var/lib/bmc` 预置为 `65532:65532`，命名卷首次挂载会继承该属主，直接启动即可。
> - **坚持宿主机目录 bind mount**（如 `./bmc-server-data:/var/lib/bmc`）：bind mount 只做覆盖、不继承镜像属主，宿主机目录默认 `root:root`，启动前必须改归属，否则容器以 `[FATAL] instance id: open /var/lib/bmc/instance_id: permission denied` 退出，并在 `restart: unless-stopped` 下反复重启：
>   ```sh
>   mkdir -p ./bmc-server-data && sudo chown -R 65532:65532 ./bmc-server-data
>   ```

> **`/tmp` 必须对 uid `65532` 可写（尤其是在 `read_only: true` 时）**：模板通过 `tmpfs: - /tmp:uid=65532,gid=65532,mode=0700` 提供可写临时目录。**只写 `mode=0700` 而漏掉 `uid`/`gid` 会让 tmpfs 归 `root:root`，非 root 进程无法写入**——SQLite 拿不到临时文件目录，报 `disk I/O error (6410)`（`SQLITE_IOERR_GETTEMPPATH`），表现为运行列表接口在较大 `limit`（200–500）时返回 500、需要临时文件的迁移/清理操作失败。手工 `docker run` 部署时同理，务必带上 `--tmpfs /tmp:uid=65532,gid=65532,mode=0700`。

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
# 1. 设置 BMC_SERVER_GRPC_URL（必须带 scheme，例如 https://backup.example.com:9090 或 http://host:9090）
# 2. 填写从 Server Web UI 获取的一次性 BMC_ENROLLMENT_TOKEN
# 3. 按需调整 BMC_SOURCE_ETC、BMC_SOURCE_SRV 及 BMC_RESTORE_ROOT 宿主机挂载路径
docker compose --env-file deploy/.env.agent -f deploy/docker-compose.agent.yml up -d --build
```

**注意事项**：
- Agent 首次注册成功后，建议清空 `.env.agent` 中的 `BMC_ENROLLMENT_TOKEN` 并重建容器（`docker compose --env-file deploy/.env.agent -f deploy/docker-compose.agent.yml up -d`）。
- 备份源目录（`/etc`、`/srv`）以只读方式（`:ro`）挂载；恢复目标目录（默认宿主机 `./backup-restore`，请按需改为绝对路径）以读写方式挂载到容器内 `/backup-restore`。
- **备份/恢复路径约定（仅两个环境变量）**：
  - `BMC_SOURCE_ROOTS`：容器内备份源根目录，可多个用逗号分隔，默认 `/backup-sources`。计划的源路径可直接写宿主机路径（`/etc` → 容器内 `/backup-sources/etc`），约定把宿主机目录挂到同名容器路径 `/backup-sources/<同名路径>`。
  - `BMC_RESTORE_ROOT`：单个恢复目标根目录，默认 `/backup-restore`；恢复的文件都落在此目录下。容器内恢复目录固定为 `/backup-restore`，宿主机目录挂载到该路径即可（默认挂载 `./backup-restore`）。
  - 裸机运行时：`BMC_SOURCE_ROOTS` 缺省不限制源路径；恢复根缺省为 `/backup-restore`，可显式设置 `BMC_RESTORE_ROOT` 指向其他目录（此时恢复目标必须落在该目录内）。
- `bmc-agent-state` 卷用于持久化 Agent 身份、restic 缓存与暂存目录（`/var/lib/bmc-agent` 下的 `identity.json`、`.cache/restic`、`scratch`），不可多主机共享。
- **暂存空间守卫 `BMC_SCRATCH_MIN_FREE_BYTES`（默认 0）**：数据库类逻辑备份在导出前校验暂存目录可用空间，要求 `>= max(预估导出字节 × 1.3, 本值)`；不足则直接以 `insufficient_temp_space` 失败并且**不产生半截 dump**。默认 0 表示只按计划中的预估导出大小判断；整实例或大库场景建议按目标体积显式上调。
- **并发度 `BMC_AGENT_MAX_CONCURRENCY`（默认 2）**：同一 Agent 同时执行的运行数上限；连接远端仓库或磁盘较慢时下调可减少争用。
- **重新安装接管（Takeover）**：若 Agent 状态卷丢失或需更换新机器接管原 Agent 数据，请在 Server Web UI 的 Agent 列表（离线状态）点击“重新安装接管”生成专用令牌，并在 `.env.agent` 中设置 `BMC_TARGET_AGENT_ID=<原AgentID>` 与 `BMC_ENROLLMENT_TOKEN=<接管令牌>`。启动后服务端会自动复用原 ID、保留全部仓库与计划并轮换密钥，接管成功后同样建议清空这两个变量。
- **备份/恢复环境变量精简**：推荐 Compose 模板不再透传 `BMC_SOURCE_PATH_MAPPINGS`、`BMC_RESTORE_PATH_MAPPINGS`、`BMC_RESTORE_ROOTS`。源路径按「宿主机路径 ↔ `/backup-sources` + 宿主机路径」自动映射，既有计划无需修改；已在 `/backup-sources` 内的容器路径保持原样；显式设置 `BMC_SOURCE_PATH_MAPPINGS` 时仍按显式映射处理（非镜像挂载布局请显式配置）。恢复白名单由 `BMC_RESTORE_ROOT` 推导为 `/backup-restore`。注意：源侧映射不再上报给 Server，计划表单不再显示“可用宿主机路径”提示，运行日志中的源路径按容器内路径显示。
- **Agent 配置精简与已有卷升级**：推荐 Compose 模板已精简 `BMC_SERVER_TLS`、`BMC_AGENT_STATE_DIR`、`BMC_RESTIC_CACHE_DIR`、`BMC_AGENT_DATA_DIR`。Agent 状态目录由镜像默认固定为 `/var/lib/bmc-agent`，缓存（`/var/lib/bmc-agent/.cache/restic`）与暂存（`/var/lib/bmc-agent/scratch`）自动基于状态目录推导。升级时保留原 `bmc-agent-state` 卷即可无缝延续身份凭据与缓存；原 `bmc-agent-scratch` 卷不再使用，其内容为临时暂存文件，可安全删除。
- **数据库客户端版本下限**：Agent 镜像内置的数据库客户端版本必须不低于目标服务端，且 MySQL 必须使用官方二进制而非 MariaDB 客户端。
  - MySQL：镜像内置官方 `mysql`/`mysqldump`。若误用 Debian 的 `mariadb-client`（`default-mysql-client` 的实体），dump 前会探测 `information_schema.columns.generation_expression`，而该列在 **MySQL 5.6 及更早**不存在，导致备份必然以 `Unknown column 'generation_expression' in 'field list' (1054)` 失败。
  - MariaDB：同样使用官方 MySQL 客户端，并且在参数中显式关闭 `--column-statistics`（`--column-statistics=0`）。官方 MySQL 8.0 客户端默认开启该选项，会先查 `information_schema.COLUMN_STATISTICS`，而该表是 MySQL 8.0 专有的，**MariaDB 与 MySQL 5.x 都没有**，dump 会以 `Unknown table 'COLUMN_STATISTICS' in information_schema (1109)`（exit 2）失败。因此镜像必须使用官方 MySQL 客户端：`mariadb-dump` 不支持该参数，无法同时兼容 MySQL 5.6 与 MariaDB。
  - **按服务端版本选择 MySQL 客户端。** 镜像同时内置官方 8.0 与 5.7 两份客户端（`mysql`/`mysqldump` 与 `mysql57`/`mysqldump57`），适配器在每次备份/恢复前查询服务端 `VERSION()` 后选择：
    - MySQL ≥8.0：用 8.0 客户端（避免旧客户端漏掉 8.0 的新对象类型），并显式传 `--column-statistics=0`。
    - MariaDB 与 MySQL <8.0：用 5.7 客户端。
    - 原因：官方 8.0 客户端请求 `utf8mb4` 时会带上 8.0 专有的默认排序规则 `utf8mb4_0900_ai_ci`；MySQL ≤5.7 不认识该排序规则，连接字符集被**回退到 latin1**，于是非 ASCII 标识符（库名/表名/列名）被错误解释：库名报 `Unknown database '<库名>'`（库其实存在），表名报 `show create table \`???\``。5.7 客户端在旧服务端与 MariaDB 上不存在该回退，且不会探测 `COLUMN_STATISTICS`。
    - 若镜像未提供 5.7 客户端或版本探测失败，则退回 8.0 客户端并保持既有行为，不阻断备份；此时遇到上述错误会额外输出可诊断告警（指明字符集回退而非库不存在）。
    - 不要用 `utf8mb3` 规避：它虽能解析标识符，但会损坏 4 字节字符（emoji 被写成 `?`）。
    - 普通 ASCII 标识符 + 非 ASCII **数据**不受影响：中文/emoji 数据在 latin1 连接下仍能完整备份与还原。
    - **跨版本恢复不受支持（MySQL 自身限制）**：由 MySQL 8.0 备份出的 dump 内嵌 `utf8mb4_0900_ai_ci` 等 8.0 专有排序规则，导入 MySQL ≤5.7 会以 `ERROR 1273 Unknown collation` 失败。此类失败会自动回滚到修改前状态（见下），并按需人工处理。恢复请使用与服务端主版本匹配的 dump。
    - **库内含非确定性例程时的特权要求**：当服务端 `log_bin=ON` 且 `log_bin_trust_function_creators=0` 时，`mysqldump --routines` 读取非确定性 `FUNCTION` 定义本身就需要 `SUPER`/`SET_USER_ID`。实测（2026-10-09）用普通账号备份这类库会以
      `mysqldump: <user> has insufficient privileges to SHOW CREATE FUNCTION \`<name>\`!` 失败，且**不会产生快照**（run 记为 `backup_failed`）。给备份账号授予 `SUPER`/`SET_USER_ID`，或临时开启 `log_bin_trust_function_creators`，即可正常备份。
    - **恢复此类 dump 同样需要该特权**：非 `SUPER` 用户导入含非确定性函数的 dump 会在建函数处报 `ERROR 1227`；实测运行以 `restore_import_failed` 失败、阶段为安全终态 `new_target_cleaned`、**目标库已被清理且不阻塞仓库**。
    - **恢复账号对目标库本身无权限时**：`CREATE DATABASE` 与回滚用的 `DROP DATABASE IF EXISTS` 会双双被 `ERROR 1044` 拒绝，此时无法证明目标未被修改，运行以 `rollback_failed` 结束并**阻塞来源仓库**（目标实际未被创建，可用 `/api/v1/restores/{id}/resolve` 人工确认解除）。请确保恢复账号具备目标库的建库/删库权限。
  - PostgreSQL：`pg_dump` 要求客户端主版本 ≥ 服务端主版本（不满足时直接 `aborting because of server version mismatch`）。Debian bookworm 自带 `postgresql-client-15`，只能备份 PG 15 及更旧的服务端；镜像改为从 PGDG 安装 `postgresql-client-${PG_CLIENT_MAJOR}`（默认 18），可覆盖更旧的服务端。构建参数 `PG_CLIENT_MAJOR`、`PGDG_BASE_URL`（默认阿里云 PGDG 镜像）可按目标环境调整。
  - PostgreSQL 恢复的版本偏斜：`pg_dump`/`pg_restore` 自 17 起会在归档前置写入 `SET transaction_timeout = 0;`，该 GUC 在 **PG 16 及更早**不存在。恢复到旧服务端时这条语句会报 `unrecognized configuration parameter`，而 `pg_restore` 只要忽略过任何错误就以 exit 1 结束。BMC 因此不再使用 `--exit-on-error`，改为只放行这一条已知无害语句，其余错误照旧失败，正确性由恢复后的关系集合校验兜底。
  - MongoDB：`mongodump`/`mongorestore` 来自官方 Database Tools，另外单独安装 `mongosh`（`MONGOSH_VERSION`）。恢复需要 `mongosh` 判断目标库是否存在并校验集合是否落地；缺失时适配器直接拒绝恢复，能力探测与恢复前置校验都会把 `mongosh` 列为必需工具。mongorestore 的 `--dryRun` 输出全部写在 stderr，校验必须同时收集 stderr，只读 stdout 会把每次恢复都判成失败。
  - PostgreSQL 恢复校验口径：`pg_restore -l` 的 TOC 类型可能是多个词（`TABLE DATA`/`SEQUENCE OWNED BY`/`SEQUENCE SET`），schema/name/owner 恒为末尾三项；目标侧查询必须与归档口径一致——统计显式索引、排除约束（PK/UNIQUE）背后的隐式索引与 `pg_toast*` 自动结构，否则会误报 `missing`/`unexpected`。
  - 构建期间对 GitHub/厂商 CDN 的下载（restic、rclone、MongoDB Database Tools、mongosh、MySQL 客户端）统一走 `fetch` 包装的重试逻辑，避免国内网络偶发连接重置导致整次构建失败。
- **旧版 Agent 配置升级注意**：若旧部署 `.env.agent` 使用不带 scheme 的裸 `host:port` 并配合 `BMC_SERVER_TLS=0`，升级前必须先将 URL 改为 `http://host:port`（若此前为 `BMC_SERVER_TLS=1` 则改为 `https://host:port`）。由于新 Compose 不再透传 `BMC_SERVER_TLS`，未带 scheme 的地址默认启用 TLS，直接升级明文连接将导致握手失败。
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
- **运行去重升级**：Server 启动时自动为 `runs` 增加可空 `dedup_key` 和活跃运行唯一索引；已有运行记录不改写，旧记录仍可读取，重复启动不会重复迁移。新提交的同参数任务在排队、下发或执行期间复用已有运行，终态后可再次提交。升级前停止 Server 并备份数据目录中的 `bmc.db`（包含可能存在的 `bmc.db-wal`/`bmc.db-shm`）及 `master.key`，备份位置由运维自行选择；启动时若存在数据库，程序还会在数据目录创建 `bmc.db.pre-migration-<UTC时间>.bak`。回滚时停止 Server，恢复升级前数据库与主密钥备份，再启动旧版本；不建议旧版本继续写入已升级的数据库。先升级 Server、确认 `/health/ready` 返回 200，再升级 Agent。
- **运行日志标识升级**：Server 启动时自动把 `run_logs` 主键从 `(run_id, seq)` 改为自增 `id`，并新增 `source`（`agent`/`server`）与 `source_seq` 两列及 `(run_id, source, source_seq)` 唯一索引。历史行按原 `seq` 升序保留并重新分配 `id`，其中 `seq >= 2^62` 的行（旧 dispatcher 的服务端诊断日志高位段）回填为 `source='server'`，其余为 `agent`；日志内容、时间戳与来源序号均不改写，重复启动不会重复迁移。REST 分页参数由 `before_seq` 改为 `before_id`（旧参数被接受但忽略），Agent 协议 `LogEntry` 新增可选 `source` 字段：未升级的 Agent 留空时按 `agent` 归类，因此**可以先升级 Server 再滚动升级 Agent**。如需回滚到旧版本 Server，必须恢复升级前的数据库备份。
- **仓库状态与绑定重试**：`POST /api/v1/repositories` 会同步等待 Agent 完成 restic 探测/初始化，状态因此有三种：`ready`（可用）、`error`（探测或初始化失败：密码不符、仓库被锁、远端不可达、路径不可写等）、以及绑定进行中的临时状态。
  - **`error` 的连带影响**：该仓库的**每周完整性校验与孤儿扫描都会停止**（两者都要求 `ready`），也不能作为跨 Agent 恢复的来源。备份/恢复本身仍可尝试，但仓库不可用时同样会失败。
  - **恢复方式**：用**同一 Agent + 同一存储目标重试绑定**——会重新执行探测/初始化，成功后自动回到 `ready`（实测：瞬时故障导致 `error` 后重试绑定即恢复 `ready` 并建好远端仓库）。
  - 若绑定返回 `504 wait_timeout`（消息为 "may still complete - retry"），Agent 侧的初始化**可能随后成功**；此时服务端依据该运行的终态结果自动把状态置为 `ready`，无需人工干预。
- **Agent 撤销与恢复**：Web「Agent」页面的「撤销」会立即断开该 Agent 的连接并拒绝其后续重连，但保留其身份与已有仓库、计划、运行记录引用。误操作时可在同页点击「恢复」（等价于 `POST /api/v1/agents/{id}/restore`），恢复后重启 Agent 进程即可用原身份重连，不会产生新的 Agent ID，也不需要重新注册。
- **下线主机时的操作顺序（重要）**：删除计划及其备份（`POST /api/v1/plans/{id}/backups/delete` 与 `DELETE /api/v1/plans/{id}`）都需要派发任务到该计划的 Agent，**由 Agent 核对并清理远端快照**后才能完成——这是为了避免静默遗留无法归属的远端数据。因此主机永久下线时**必须先在 Agent 在线状态下删除其计划与备份，再撤销 Agent**；反之（先撤销/离线再删计划）接口会等待至该计划的 `timeout_seconds` 后以 `422 agent_unavailable` 失败，`DELETE /plans/{id}` 返回 `504 wait_timeout`，计划记录保持存在且无法删除。若 Agent 身份仍在（未删除其状态目录），可用 `POST /api/v1/enrollment-tokens` 带 `target_agent_id` 签发定向 token，让该主机上的 Agent 以**原身份**重新注册上线，再按上述顺序清理。
- **数据保留**：日常维护使用 `docker compose down` 停止容器，数据卷不会丢失；**严禁使用 `down -v`**，否则会永久销毁数据库及生成的本地主密钥。
- **孤儿扫描退避（`0015_snapshot_cleanup_backoff.sql`）**：`snapshot_cleanup_state` 新增 `next_attempt_at` 列。此前孤儿扫描失败后状态表没有退避字段，`ClearSnapshotCleanupScan` 传入的退避时间被静默忽略，导致扫描持续失败的仓库在每个 scheduler tick（15 秒）重发一次 `snapshots`，7 天可累积数万 run 与数百万行日志，并最终因 restic/rclone 进程耗尽出现 `fork/exec ... resource temporarily unavailable`。升级后扫描失败按 1 小时退避重试，成功则清除退避并按 24 小时周期扫描。迁移为纯增量加列，可重复启动安全。

## 6. 跨 Agent 恢复与数据库恢复安全边界

「快照与恢复」页面支持把来源 Agent 仓库中的快照恢复到**另一个在线 Agent**（目标 Agent），文件与单库数据库均适用。

- **API 契约**：`POST /api/v1/restores` 与 `POST /api/v1/restores/dry-run` 新增可选 `target_agent_id`。缺省或空值沿用来源仓库所属 Agent（既有调用方行为不变），非空则把恢复 run 绑定到该 Agent。跨 Agent 恢复要求来源仓库 `status=ready`、目标 Agent 存在且在线未撤销。
- **凭据边界**：仓库路径、rclone 配置与 restic 密码始终来自来源仓库（不重绑、不改仓库路径中的来源 ID），但会**传递给目标 Agent**执行；恢复向导会明确提示这一点。
- **存储后端必须两端可达**：跨 Agent 恢复由目标 Agent 访问来源仓库，因此存储后端必须对两个 Agent 都可见。`rclone` 的 `local` 后端是**节点本地**的（路径解析在各自容器/主机上），只适合单 Agent；跨 Agent 请使用对象存储或共享文件系统（S3/WebDAV/SFTP，或把同一共享目录挂载到两个 Agent 的相同路径）。对 `local` 后端做跨 Agent 恢复会在目标 Agent 上以 `repository_missing`（restic exit 10）失败。
- **本地仓库路径必须为绝对路径**：当存储目标的 rclone remote 是 `local` 类型时，`remote_path` 必须以 `/` 开头。rclone 的 local 后端按进程工作目录解析相对路径，相对 `remote_path` 会让仓库落在 Agent 运行时的临时目录下，表现为运行失败并报 `restic exit 10 (repository_missing) ... repository does not exist`。Server 会保留操作者填写的绝对路径（`<remote>:/abs/path/<instanceID>/<agentID>`）；对象存储类 remote 的桶路径（如 `bucket/path`）不带前导斜杠，行为不变。
- **快照授权**：只能从服务端持久化的**已验证**快照列表/目录缓存中选取（来源 Agent 离线时同样适用），并核对快照指纹、存在性、未隐藏与 `kind:` 标签。缓存缺失、损坏、生成号变化或元数据查询失败一律返回 HTTP 409 `snapshot_list_refresh_required`，要求先刷新列表。
- **能力授权**：目标 Agent 必须在其**当前连接**上报 `safe_database_restore=true`。连接尚未上报能力返回 409 `agent_capabilities_pending`；明确不支持返回 422 `agent_upgrade_required`，请先升级 Agent。持久化的能力仅用于展示，不作为执行授权。
- **系统库目标**：mysql（mysql/information_schema/performance_schema/sys）、postgresql（postgres/template0/template1）、mongodb（admin/local/config）在**受理阶段**即被拒绝（422 `path_validation_failed`），不会创建 run、不占用全局恢复占用；各适配器在准备目标时同样拒绝，作为纵深防御。
- **数据库范围**：整实例还原（`database=all`）同步返回 422 `unsupported_restore_manifest`；来源快照若包含多库或 globals（下载后才能判断），已返回 202 的请求会以 run 错误码 `unsupported_restore_manifest`、`phase=failed` 结束，且目标未被修改。
- **覆盖恢复需确认值**：`overwrite=true` 的数据库恢复必须带 `confirmation`，其值为目标库名的 SHA-256 十六进制（服务端 `secrets.HashToken`，无密钥）；不匹配或缺失时同步返回 403 `forbidden`。响应文案刻意保持简短，不区分是哪个目标字段有问题（避免被用来枚举目标）。
- **每种数据库 kind 默认禁用**：数据库恢复会覆盖数据，必须先在**真实隔离实例**上验证预备份与回滚，再通过 `BMC_DATABASE_RESTORE_KINDS=postgresql,mysql,mongodb,sqlite` 逐项启用；未启用的 kind 返回 503 `database_restore_disabled`。`sqlite`（Go 实现）不需要数据库客户端。
- **文件系统恢复的覆盖模式**：`never` 只允许写入空目标（目标非空则拒绝 `restore_target_not_empty`）；`if-changed` 按 restic 的 **size + mtime** 判定，**内容变了但这两者未变时不会覆盖**（同长度改写并保留 mtime 的场景需用 `always`）；`always` 全量覆盖快照内的条目，但**不删除**目标中快照之外的多余文件（是"覆盖"而非"镜像"）。dry-run 与真实恢复共用同一套前置校验（含 never 的非空检查），预演结果与真实结局一致。
- **文件系统恢复的目标级互斥**：同一 Agent 上，**同一目标路径**不得同时存在两个未终结的文件系统恢复（`409 restore_target_busy`）。两个恢复并发写同一目录会产生非确定结果，并可能同时通过 `overwrite_mode=never` 的"目标为空"前置检查（实测：两个不同快照并发恢复到同一目录时二者均成功）。校验与插入在同一事务内完成，不存在 TOCTOU；数据库恢复由全局占用（`database_restore_busy`）保护，不受此限。
- **全局串行**：同一时间只允许一个数据库恢复（占用从 queued 持续到明确的安全终态）。其他数据库恢复返回 409 `database_restore_busy`；相同参数提交会复用已有任务。`rollback_failed`/`manual_recovery_required` 不释放占用，Server 重启后从持久化状态恢复占用并阻塞对应来源仓库的后续命令。
  - **阶段语义与阻塞范围**：安全终态为 `succeeded` / `failed` / `pre_backup_failed` / `rolled_back` / `new_target_cleaned`；其中 **`failed` 的语义是"执行失败且已确认目标未被修改"**，因此**目标被触碰之前**的失败（清单范围校验、适配器不匹配、快照下载失败、目标已存在且未开启覆盖等）都归入该阶段，不占用仓库、无需人工介入。`rollback_failed` / `manual_recovery_required` 表示"无法证明目标未被修改"，会阻塞该来源仓库的**全部**后续任务（含备份与快照列表浏览），必须由 `/api/v1/restores/{id}/resolve` 人工确认后解除。
  - **失败码与回滚结果的组合**：回滚失败一律报 `rollback_failed`；回滚成功（`rolled_back` / `new_target_cleaned`）时按失败类别区分——**导入步骤本身失败报 `restore_import_failed`**，其余"恢复未能建立或未能验证"的情形报 `restore_verification_failed`（含目标清单不可用、**无法判定目标是否存在**）。两者目标都已回到修改前状态、都不占用仓库，但排查方向不同。
  - **滚动升级顺序**：上述"目标被触碰前的失败归入 `failed`"依赖 Agent 在结果中回报阶段。**升级前的旧 Agent** 不回传该字段，其前置失败仍会被 Server 保守判为 `manual_recovery_required` 并阻塞来源仓库。因此升级时必须**先升级 Agent、再依赖该行为**；旧 Agent 存续期间若出现仓库阻塞，按人工解除流程处理（数据安全不受影响，仅需一次确认）。
  - 排查提示：若某仓库的备份长期停在 `queued`、快照列表接口返回 504，先查是否存在 `rollback_failed` / `manual_recovery_required` 的恢复记录——这正是该仓库被阻塞的表现。
- **保护快照**：覆盖已有目标前，先把目标现状导出并上传到**来源仓库**，仅打 `restore-protection:<runID>` 与 `kind:<kind>` 标签（不含 `plan:`/`run:`）。该快照不会被删除请求移除（即使尚无 ID 引用），也不被保留策略与孤儿扫描认领，用于人工定位回滚点。
- **维护要求（运维前提）**：
  - 网络数据库：恢复期间必须**隔离外部写入**；导入失败会自动回滚，进程/主机突然中断则记录 `manual_recovery_required`，不承诺跨重启自动回滚。
  - SQLite：必须**关闭目标数据库的全部连接**并使应用的 WAL 完成 checkpoint；无法建立独占维护窗口时在执行前拒绝。
  - PostgreSQL：覆盖时完整重建目标库（DROP/CREATE），需要当前角色是 owner 或 superuser，否则在破坏性步骤前拒绝；不修改集群全局角色或其他数据库。
- **取消与超时**：已下发的恢复在收到取消/超时后先请求 Agent 停止，保持非终态并记录 `cancelling`，等待 Agent 完成回滚/清理并回报结果；超过宽限或失去执行连接后记录 `manual_recovery_required`，绝不自动重试破坏性恢复。
- **人工解除**：`POST /api/v1/restores/{id}/resolve` 接受 `manual_recovery_required`/`rollback_failed` 的请求，需要 `run_id`、处理说明，以及 `execution_stopped` 与 `target_verified` 两项确认；同一事务写入 `manual_recovery_resolved` 与审计记录。该接口是人工确认，不是自动探测或自动回滚，且不能通过改数据库或重启服务隐式解锁。
- **升级顺序**：协议仅新增可选 capability 字段与 JSON 字段，Server 必须先升级；旧 Agent 仍可连接并执行原有备份与文件恢复，只是不能作为数据库恢复的目标。

## 7. 管理员密码重置

如果忘记 Server 管理员登录密码，可通过 `backup-center-server reset-admin` 命令清除管理员账号及活跃会话，重新触发 Web 引导初始化流程（此操作不会删除存储目标、备份计划、仓库及运行记录）：

- **Docker Compose 环境**：
  ```sh
  # 停止运行中的 Server 容器
  docker compose --env-file deploy/.env -f deploy/docker-compose.yml stop bmc-server

  # 使用 reset-admin 命令清除管理员信息并重新开放 Web 引导
  docker compose --env-file deploy/.env -f deploy/docker-compose.yml run --rm bmc-server reset-admin

  # 重新启动 Server
  docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d bmc-server
  ```
- **本地二进制或 systemd 环境**：
  ```sh
  # 停止服务后执行
  BMC_DATA_DIR=/var/lib/bmc backup-center-server reset-admin
  # 启动服务后访问 Web 控制台即可重新进行初始化设置
  ```
