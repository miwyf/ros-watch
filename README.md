# ROS Watch

单容器 RouterOS 外部访问观察台：来源 / 目标端口排行、访问趋势、疑似扫描、明细筛选与 CSV 导出。界面适配电脑和手机，静态资源全部本地提供。

版本 **0.2.0**。Python 3.13、Flask + Waitress、SQLite，一个进程，无独立数据库或采集器。

## Docker / 1Panel 部署

在项目目录直接运行：

```sh
docker compose up -d --build
```

**无需填写 .env、本机 IP、长期密码或采集参数。** 默认发布网页 TCP 8086、syslog UDP 5514；需要改端口时修改 `compose.yaml` 的 `ports`。容器监听自己的所有接口，由 Docker 负责宿主机映射。将其部署在可信内网，宿主机防火墙应限制日志来源；无需为日志端口新增 WAN 映射。

首次启动自动创建用户名 `admin` 和随机密码。初始凭据仅保存在数据卷的 `/data/initial-admin.txt`，不会打印到容器日志，可在服务器本地查看：

```sh
docker compose exec ros-watch cat /data/initial-admin.txt
```

访问 `http://<服务器内网地址>:8086` 登录，进入 **系统设置**：

1. 在“账号安全”修改用户名或密码。修改时必须验证当前密码，修改后所有旧登录失效；首次修改成功后初始凭据文件自动移除。
2. 在“日志与观察规则”填写允许发送日志的路由器地址和 WAN 接口映射。
3. 设置保留期限、容量预算、队列、接收限速、扫描阈值及 ROS 采样情况，保存后生效。白名单未配置时拒绝所有来源，不开放匿名初始化。

数据库仅存储 Argon2id 密码哈希（独立随机盐，19 MiB / 2 次迭代 / 1 并行度），不保存可取回的明文密码。账号和设置在 `/data/control.sqlite3`，日志在 `/data/events.sqlite3`，会话密钥在 `/data/session.key`，全部位于持久命名卷。改变环境变量不会覆盖后台已保存的账号或参数。

1Panel：在“容器 → 编排”使用本项目 `compose.yaml`，保留 Dockerfile、requirements.txt、roswatch/。镜像 `ros-watch:0.2.0` 为本地构建，未发布公共仓库。也可直接用 Compose 部署后在 1Panel 容器页管理。

容器以非 root 运行，只读根文件系统，仅 `/data` 可写，默认限制 192 MiB 内存 / 0.5 CPU。不要执行 `docker compose down -v`，它会删除账号、设置和日志所在的数据卷。

## 后台可修改的参数

| 项目           |                           默认值 | 范围 / 说明                                    |
| -------------- | -------------------------------: | ---------------------------------------------- |
| 日志来源白名单 |                               空 | 支持 IP/CIDR，空表示暂停接收，禁止全部地址网段 |
| WAN 映射       | pppoe-out1=WAN1、pppoe-out2=WAN2 | 名称调整只影响后续记录                         |
| 最长保留       |                             7 天 | 1–31 天                                        |
| 保留行数       |                           500000 | 1–2000000，每约 30 秒清理，可能短暂超出        |
| 主库预算       |                           512 MB | 8–8192 MB，80% 使用量触发清理旧数据            |
| 最低可用空间   |                           256 MB | 空间不足时暂停写入，界面显示丢弃               |
| 接收处理上限   |                        500 条/秒 | 1–5000，超出计入处理限速丢弃                   |
| 等待队列       |                          4096 条 | 1–8192，调整容量时保留已排队记录               |
| 扫描端口阈值   |                               15 | 2–1000 个不同端口                              |
| 扫描窗口       |                           300 秒 | 60–3600 秒，固定窗口                           |
| ROS 端采样情况 |                           未确认 | 只标记统计口径，不修改 ROS                     |

降低期限、行数或预算会清理旧记录。主库预算不等于整个卷的硬配额：WAL/SHM、账号库和文件系统也占空间；删除后数据页可复用，文件不一定立即缩小。容器自身日志已独立轮转（5 MiB × 2）。Debian / ESXi 数据存储仍需预留空间。

默认部署没有业务环境变量。只有部署层面的 `DATA_DIR`、`HTTP_HOST/PORT`、`SYSLOG_HOST/PORT`、`COOKIE_SECURE`、`DEMO_MODE` 可在需要时通过 Compose 覆盖。它们不包含真实宿主机 IP 或登录凭据。HTTPS 由反向代理提供时，确认已通过 HTTPS 访问后再设置 `COOKIE_SECURE=true`；HTTP 保持 false。

## 从 0.1.0 升级

旧版账号只有密码，业务参数在容器环境中。**先迁移再更换容器**，保留同一命名卷和 Compose 项目名。一次性管理命令可从标准输入读取旧版有效环境值（JSON 对象）：

```sh
# 在挂载同一数据卷的新版本临时容器内调用；通过 stdin 输入旧配置，勿把密码放在参数里。
python -m roswatch.manage import-v01 --stdin
```

迁移将旧密码初始化为 `admin` 的密码，并导入白名单、WAN 映射、保留和限速设置。已存在账号时不会覆盖账号或重置密码。实际升级应先备份旧镜像、Compose、私人配置和数据库；将旧容器有效环境值在服务器内通过管道传给新版本，避免将凭据输出到终端或报告。

迁移成功后使用新 Compose 重建服务，移走活动 `.env` 中旧的业务和密码项。原有日志库不变；新建的 `control.sqlite3` 也须纳入备份。私人旧配置只作为受保护的回退资料保留，不加入源码或共享报告。

## 忘记密码

在服务器的项目目录执行交互式恢复：

```sh
docker compose exec ros-watch python -m roswatch.manage reset-password
```

命令通过隐藏输入读取新密码，不接受命令行密码参数。需要同时恢复用户名时追加 `--username 新用户名`。恢复会让所有旧网页登录失效，不删除日志。

## 接入 RouterOS

将 ROS 防火墙 syslog 发到服务器 UDP 5514。记录应包含 `input:` / `forward:`、入站接口、协议和 `来源->目标`；普通系统日志不能生成访问统计。

`routeros/ros-watch.rsc.example` 是待审核、手动应用的模板，默认文档地址直接导入会停止。项目不会自动登录或封禁路由器。模板新增独立 `action=log` 规则，四条规则分别限制 10 条/秒、突发 20 条，限速只影响日志，不应添加到现有 drop 规则上。

- `RW-INPUT`：访问路由器的尝试。
- `RW-FWD`：经过 dstnat 的转发尝试。
- `RW-DROP`：只有日志规则确实放在匹配的 drop 前时才使用，显示为“拦截前记录”。
- 其他普通防火墙日志：入站接口存在于后台 WAN 映射时接收，类别为“访问尝试”。

接入后在“接收状态”核对有效记录与忽略原因。标准 Linux Docker bridge 发布 UDP 端口通常保留远端源地址，应填写 ROS 实际发送来源；不要为了排查而允许所有设备。

## 统计口径

- 统计为已收到且保留的日志观察次数，不是成功连接、用户或 HTTP 请求数。TCP SYN 可能重传，UDP 无反向数据时也可能持续处于 new 状态。
- 采用服务器接收时间（内部 UTC、浏览器本地时区展示），请保持系统时间同步。
- 转发目标可能已被 NAT 转换，例如外网 11883 转到内网 1883，所见目标可能显示 1883。
- 扫描按 UTC 对齐的固定窗口与同源不同目标端口数判断，可能误报或漏报，仅提示，不自动封禁。
- 处理限速、队列满、超长、低磁盘及写入失败丢弃可计数；ROS 限速和不可观测的网络/内核 UDP 丢包无法准确计数。界面明确展示采样模式。
- 运行计数在服务重启后归零，持久化记录仍保留。概览最多约 10 秒缓存；改设置会清理概览缓存。
- CSV 导出当前筛选最新 10000 条，不含原始全文。

## 更新与备份

更新源码后执行 `docker compose up -d --build`。备份包含完整持久卷中的账号库、日志库、会话密钥及需要的部署配置。可停服务后备份卷；运行时请使用 SQLite 备份 API 导出一致性副本，不要只复制正在写入的单个日志库而忽略 WAL。含密码哈希、日志地址和初始凭据的备份也应按私人数据保护。

## 开发验证

Python 3.13+；安装 `requirements-dev.txt` 后执行：

```sh
python -m pytest -q -p no:cacheprovider
docker build --target test -t ros-watch-tests .
```

测试依赖不进入生产镜像。使用项目外的临时数据目录运行 `python -m roswatch`；首次凭据保存在该目录。设置 `DEMO_MODE=true` 和 `HTTP_HOST=127.0.0.1` 可生成文档地址演示数据（必须绑定回环），不要把演示库接入生产。

`tools/send_sample.py` / `tools/load_probe.py` 仅供有限模拟验证，不要向生产数据库发送模拟流量。工具临时认证参数与服务的持久账号配置相互独立。

## 官方资料

- [RouterOS 日志](https://manual.mikrotik.com/docs/diagnostics-monitoring-and-troubleshooting/log/)
- [Flask 使用 Waitress 部署](https://flask.palletsprojects.com/en/stable/deploying/waitress/)
- [OWASP 密码存储](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html)
- [Docker 环境变量与 Secrets](https://docs.docker.com/compose/how-tos/environment-variables/best-practices/)
