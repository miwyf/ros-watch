# ROS Watch

单容器 RouterOS 外部访问观察台。接收防火墙 syslog，自动整理来源排行、目标端口排行、时间趋势和疑似扫描；支持来源 IP / 端口 / WAN / 协议 / 访问对象筛选、原始记录展开和 CSV 导出。界面适配电脑与手机，无 CDN、外部字体或第三方统计请求。

版本：**0.1.0**。Python 3.13、Flask + Waitress、SQLite；一个服务进程，无独立数据库、采集器或前端构建服务。

## Docker / 1Panel 部署

将项目完整放到新 Debian 上，例如 `/opt/ros-watch`。首次在该目录运行：

```sh
cp .env.example .env
```

编辑 `.env`，至少填写：

- `BIND_IP`：Debian 的内网地址。默认 `127.0.0.1` 只能本机访问，ROS 无法远程发送。
- `APP_PASSWORD`：随机生成、至少 12 位的看板密码。与路由器密码无关。
- `ALLOWED_SENDERS`：**实际发送 syslog 的 ROS 内网地址**，建议精确到单个 IP；逗号分隔可添加多个 IP/CIDR。禁止填写外部访问者的 IP，也不要直接允许所有来源。
- `WAN_MAP`：RouterOS 的入站接口与显示名称，例如 `{"pppoe-out1":"WAN1","pppoe-out2":"WAN2"}`。

启动：

```sh
docker compose up -d --build
```

浏览器访问 `http://<Debian内网地址>:8086`，使用 `.env` 中的密码登录。ROS 发送到 `<Debian内网地址>:5514/UDP`。

1Panel：在“容器 → 编排”中使用此项目的 `compose.yaml`，项目目录同时保留 Dockerfile、requirements.txt、roswatch/ 和 `.env`。这是本地构建镜像 `ros-watch:0.1.0`，**不是已发布的公共镜像**。

容器仅以非 root 运行，根文件系统只读，仅命名卷 `/data` 可写；默认限制 192 MiB 内存、0.5 CPU。命名卷保存数据库与会话密钥，更新容器不会清空。不要运行 `docker compose down -v`，该选项会删除卷。

需要 HTTPS 时，在 1Panel 配置反向代理到网页端口并将 `COOKIE_SECURE=true`；直接使用 HTTP 时保持 false。UDP 日志端口仅供内网 ROS 访问，宿主机防火墙应限制来源；它不需要 WAN 端口映射。源地址白名单是接收范围限制，不是加密认证，适用于可信内网。

## 接入 RouterOS

先确认已有日志是否包含防火墙的 `input:` / `forward:`、入站接口、协议和 `来源->目标` 字段。普通系统日志、接口变化日志不能生成访问统计。

`routeros/ros-watch.rsc.example` 是**待审核、手动应用的模板**。本项目不会登录路由器、修改防火墙或执行封禁。模板默认未填写实际服务器地址，直接导入会停止。

模板以新建的独立 `action=log` 规则记录 WAN 的 TCP SYN / UDP 新状态报文，并把 `RW-` 前缀日志发往本服务。它不修改现有 accept/drop，不应把限速条件添加到现有 drop 规则。每条模板日志规则最多 10 条/秒、突发 20 条，四条规则的上限分别计算。

- `RW-INPUT`：访问路由器自身的尝试。
- `RW-FWD`：经过 dstnat 的转发尝试。
- `RW-DROP`：仅当你确认日志规则放在实际 drop 前、且条件对应拦截流量时使用；显示为“拦截前记录”。模板不会凭空判定拦截结果。
- 其他已有的普通 RouterOS 防火墙日志：只有入站接口存在于 `WAN_MAP` 才接收；记录类别为“访问尝试”。

接通后在“接收状态”查看最后收到时间、已写入数量和忽略原因；若 UDP 报文增加而记录不增加，检查格式与 WAN_MAP；“拒绝的非白名单来源”增加时检查 ROS 实际发送源和 ALLOWED_SENDERS（Docker 网络需保留发送源，标准 Linux bridge 发布 UDP 端口通常保留远端源地址）。

### 统计口径

- 数字表示**已收到且保留的日志观察次数**，不是独立成功连接数、用户数或 HTTP 请求数。TCP SYN 可能重传；UDP 未收到反向数据时也可能持续处于 new 状态。
- 数据时间为服务器的接收时间（内部 UTC，显示为浏览器本地时区），不使用 BSD syslog 中缺少年份/时区的时间。请让 Debian 开启时间同步。
- 转发日志中的目标地址/端口可能已被 NAT 转换。例如外网 11883 转到内网 1883，看板“所见目标”可能显示 1883；不能当成原始外网端口。
- 疑似扫描：默认按 **UTC 对齐的 5 分钟固定窗口、同一来源至少 15 个不同目标端口**标记。慢速、跨窗口、分布式扫描可能漏报；正常探测也可能被标记。仅提供观察结果，不自动封禁。
- ROS 日志限速导致的遗漏、网络或内核 UDP 丢包，接收端无法完整计数。默认 `SAMPLING_MODE=limited` 明示采样。`full` 只代表你确认发送端未限速，不能保证无丢包。
- 本服务处理限速、队列满、超长报文、低磁盘/写入失败造成的丢弃会在界面单独列出；运行计数在服务重启后归零，保留记录仍在数据库中。
- 概览统计有最多约 10 秒缓存。CSV 导出当前条件下最新 **10000 条**，不包含原始全文。

## 控制资源和保留量

| 配置                  | 默认值 | 说明                                             |
| --------------------- | -----: | ------------------------------------------------ |
| `RETENTION_DAYS`      |      7 | 最长保留天数                                     |
| `MAX_ROWS`            | 500000 | 保留记录数目标上限，每约 30 秒清理，可能短暂超出 |
| `MAX_DB_MB`           |    512 | SQLite 主库页数上限，80% 使用量触发旧数据清理    |
| `MIN_FREE_MB`         |    256 | 可用空间低于此值暂停入库，界面显示丢弃           |
| `INGEST_PER_SECOND`   |    500 | 全局令牌桶接收处理上限；超出报文丢弃并计数       |
| `QUEUE_SIZE`          |   4096 | 内存等待队列上限（直接环境变量设置）             |
| `SCAN_PORTS`          |     15 | 扫描识别不同端口阈值（直接环境变量设置）         |
| `SCAN_WINDOW_SECONDS` |    300 | 固定扫描窗口（直接环境变量设置）                 |

保留期、行数和容量以先达到的条件为准。数据库按最多 250 条/约 250 ms 批量提交，WAL 模式、定期 checkpoint；已删数据页可复用，主库文件不一定立即缩小。`MAX_DB_MB` **不是整个卷的硬配额**：WAL/SHM、会话密钥和文件系统开销额外占用空间，降低已有数据库的设置也不会立即缩小文件。Compose 已为容器运行日志设置独立轮转（5 MiB × 2）；宿主机/ESXi 数据存储仍需保留空间。

如必须严格隔离磁盘占用，应在 Debian 层使用有配额的目录/文件系统；仅靠 Docker 的内存限制不能限制整个磁盘。

## 更新与备份

更新源码后运行 `docker compose up -d --build`。数据库当前没有外部依赖，使用 SQLite 在线备份 API 导出一致性副本，例如：

```sh
docker compose exec ros-watch python -c "import sqlite3; a=sqlite3.connect('/data/events.sqlite3'); b=sqlite3.connect('/data/backup.sqlite3'); a.backup(b); b.close(); a.close()"
```

备份副本生成在命名卷 `/data/backup.sqlite3`，应取出到外部备份位置，并及时移走卷内副本以避免占用日志预算。备份还应包含 `.env` 和 `/data/session.key`（私人文件，禁止提交仓库）。也可停止容器后备份整个命名卷；不要直接复制正在写入的单个 sqlite3 文件而忽略 WAL。

## 本地开发与演示

Python 3.13+，建议项目外创建虚拟环境后安装 `requirements.txt`。在项目目录中设置环境变量后运行 `python -m roswatch`。

```powershell
$env:APP_PASSWORD='自行设置至少12位密码'
$env:ALLOWED_SENDERS='127.0.0.1'
$env:HTTP_HOST='127.0.0.1'
$env:SYSLOG_HOST='127.0.0.1'
$env:DATA_DIR=Join-Path $env:TEMP 'ros-watch-demo'
$env:DEMO_MODE='true'
python -m roswatch
```

访问 `http://127.0.0.1:8080`。演示使用 RFC5737 文档地址生成合成记录，页面有明显提示；演示模式禁止绑定到所有网卡。生产请关闭 DEMO_MODE，并使用全新的数据卷，不要混入合成记录。

开发验证先安装 `requirements-dev.txt`，然后运行 `python -m pytest -q -p no:cacheprovider`。Linux 容器验证可运行 `docker build --target test -t ros-watch-tests .`，测试依赖不会进入生产镜像。`tools/send_sample.py` 可向测试实例发送仅使用文档地址的 UDP 示例；不要把模拟记录发送到生产实例。`tools/load_probe.py` 用于本地限时链路与资源探测，不会访问真实路由器。

## 官方资料

- [RouterOS 日志](https://manual.mikrotik.com/docs/diagnostics-monitoring-and-troubleshooting/log/)
- [RouterOS 防火墙日志与匹配器](https://manual.mikrotik.com/docs/firewall-and-quality-of-service/firewall/common-firewall-matchers-and-actions/)
- [Flask 使用 Waitress 部署](https://flask.palletsprojects.com/en/stable/deploying/waitress/)
