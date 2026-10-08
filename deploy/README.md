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
- **重新安装接管（Takeover）**：若 Agent 状态卷丢失或需更换新机器接管原 Agent 数据，请在 Server Web UI 的 Agent 列表（离线状态）点击“重新安装接管”生成专用令牌，并在 `.env.agent` 中设置 `BMC_TARGET_AGENT_ID=<原AgentID>` 与 `BMC_ENROLLMENT_TOKEN=<接管令牌>`。启动后服务端会自动复用原 ID、保留全部仓库与计划并轮换密钥，接管成功后同样建议清空这两个变量。
- **备份/恢复环境变量精简**：推荐 Compose 模板不再透传 `BMC_SOURCE_PATH_MAPPINGS`、`BMC_RESTORE_PATH_MAPPINGS`、`BMC_RESTORE_ROOTS`。源路径按「宿主机路径 ↔ `/backup-sources` + 宿主机路径」自动映射，既有计划无需修改；已在 `/backup-sources` 内的容器路径保持原样；显式设置 `BMC_SOURCE_PATH_MAPPINGS` 时仍按显式映射处理（非镜像挂载布局请显式配置）。恢复白名单由 `BMC_RESTORE_ROOT` 推导为 `/backup-restore`。注意：源侧映射不再上报给 Server，计划表单不再显示“可用宿主机路径”提示，运行日志中的源路径按容器内路径显示。
- **Agent 配置精简与已有卷升级**：推荐 Compose 模板已精简 `BMC_SERVER_TLS`、`BMC_AGENT_STATE_DIR`、`BMC_RESTIC_CACHE_DIR`、`BMC_AGENT_DATA_DIR`。Agent 状态目录由镜像默认固定为 `/var/lib/bmc-agent`，缓存（`/var/lib/bmc-agent/.cache/restic`）与暂存（`/var/lib/bmc-agent/scratch`）自动基于状态目录推导。升级时保留原 `bmc-agent-state` 卷即可无缝延续身份凭据与缓存；原 `bmc-agent-scratch` 卷不再使用，其内容为临时暂存文件，可安全删除。
- **数据库客户端版本下限**：Agent 镜像内置的数据库客户端版本必须不低于目标服务端，且 MySQL 必须使用官方二进制而非 MariaDB 客户端。
  - MySQL：镜像内置官方 `mysql`/`mysqldump`。若误用 Debian 的 `mariadb-client`（`default-mysql-client` 的实体），dump 前会探测 `information_schema.columns.generation_expression`，而该列在 **MySQL 5.6 及更早**不存在，导致备份必然以 `Unknown column 'generation_expression' in 'field list' (1054)` 失败。
  - MariaDB：同样使用官方 MySQL 客户端，并且在参数中显式关闭 `--column-statistics`（`--column-statistics=0`）。官方 MySQL 8.0 客户端默认开启该选项，会先查 `information_schema.COLUMN_STATISTICS`，而该表是 MySQL 8.0 专有的，**MariaDB 与 MySQL 5.x 都没有**，dump 会以 `Unknown table 'COLUMN_STATISTICS' in information_schema (1109)`（exit 2）失败。因此镜像必须使用官方 MySQL 客户端：`mariadb-dump` 不支持该参数，无法同时兼容 MySQL 5.6 与 MariaDB。
  - **已知限制：MySQL ≤5.7 上非 ASCII 库名无法备份。** MySQL ≤5.7 的 `character_set_server` 默认为 `latin1`。官方 8.0 客户端请求 `utf8mb4` 时，会因 8.0 的默认排序规则 `utf8mb4_0900_ai_ci` 在旧服务端不存在而**回退到 latin1**，于是库名被按 latin1 解释，服务端报 `Unknown database '<库名>'`（库实际存在），备份以 exit 2 失败。实测该问题在 MySQL 5.5/5.6/5.7 上复现，MariaDB 11 与 MySQL 8.0 正常。
    - 客户端侧无可用修复：改用 `utf8mb3` 虽能正确解析标识符，但会**损坏 4 字节字符**（emoji 被写成 `?`），得不偿失；客户端也没有指定连接排序规则的选项。
    - 处理方式：备份会输出可诊断提示（明确指出字符集回退而非库不存在）。解决需在服务端启用 `--character-set-server=utf8mb4`，或改用 ASCII 库名。
    - 普通 ASCII 库名 + 非 ASCII **数据**不受影响：实测中文/emoji 数据在 latin1 连接下仍能完整备份与还原。
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
- **Agent 撤销与恢复**：Web「Agent」页面的「撤销」会立即断开该 Agent 的连接并拒绝其后续重连，但保留其身份与已有仓库、计划、运行记录引用。误操作时可在同页点击「恢复」（等价于 `POST /api/v1/agents/{id}/restore`），恢复后重启 Agent 进程即可用原身份重连，不会产生新的 Agent ID，也不需要重新注册。
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
- **数据库范围**：整实例还原（`database=all`）同步返回 422 `unsupported_restore_manifest`；来源快照若包含多库或 globals（下载后才能判断），已返回 202 的请求会以 run 错误码 `unsupported_restore_manifest`、`phase=failed` 结束，且目标未被修改。
- **每种数据库 kind 默认禁用**：数据库恢复会覆盖数据，必须先在**真实隔离实例**上验证预备份与回滚，再通过 `BMC_DATABASE_RESTORE_KINDS=postgresql,mysql,mongodb,sqlite` 逐项启用；未启用的 kind 返回 503 `database_restore_disabled`。`sqlite`（Go 实现）不需要数据库客户端。
- **全局串行**：同一时间只允许一个数据库恢复（占用从 queued 持续到明确的安全终态）。其他数据库恢复返回 409 `database_restore_busy`；相同参数提交会复用已有任务。`rollback_failed`/`manual_recovery_required` 不释放占用，Server 重启后从持久化状态恢复占用并阻塞对应来源仓库的后续命令。
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
