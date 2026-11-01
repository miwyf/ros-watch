# ROS Watch

单容器 RouterOS 日志与外部访问观察台：来源 / 目标端口排行、访问趋势、疑似扫描、IP 归属地、明细筛选与 CSV 导出。界面适配电脑和手机，静态资源全部本地提供。

版本 **0.6.1**。Go、内嵌网页资源和 SQLite，单个原生程序，无独立数据库或采集器。

## Docker Compose 部署（推荐）

公开镜像：`ghcr.io/miwyf/ros-watch:latest`，支持 Linux amd64 / arm64。服务器安装 Docker 和 Compose 插件后，准备 `compose.yaml`，以下两种方式任选一种：

- **下载文件**：[下载 compose.yaml](https://raw.githubusercontent.com/miwyf/ros-watch/main/compose.yaml)，保存到服务器的部署目录。也可通过命令下载：

  ```sh
  mkdir -p ros-watch
  cd ros-watch
  curl -fsSLo compose.yaml https://raw.githubusercontent.com/miwyf/ros-watch/main/compose.yaml
  ```

- **粘贴配置**：在部署目录新建 `compose.yaml`，粘贴下方 [完整 Compose 配置](#1panel-编排部署)。这份配置通用于普通 Docker Compose 和 1Panel。

在 `compose.yaml` 所在目录执行：

```sh
docker compose pull
docker compose up -d
docker compose ps
```

默认使用 `latest`，跟随 main 上通过测试的最新构建。已有容器不会自行升级，更新时需要拉取镜像并重建容器。需要固定版本或回退时，可将标签换成 `0.6.1` 等已发布版本。网页端口为 8086，日志端口为 UDP 5514。

## 1Panel 编排部署

进入 **容器 → 编排 → 创建编排**，选择直接输入 Compose 内容（不同版本界面名称可能略有差异），名称填写 `ros-watch`，粘贴下面完整配置后部署。

```yaml
services:
  ros-watch:
    image: ghcr.io/miwyf/ros-watch:latest
    restart: unless-stopped
    init: true
    ports:
      - "8086:8080"
      - "5514:5514/udp"
    volumes:
      - ros-watch-data:/data
    read_only: true
    tmpfs:
      - /tmp:size=16m,noexec,nosuid
    cap_drop: [ALL]
    security_opt:
      - no-new-privileges:true
    mem_limit: 192m
    cpus: 0.50
    pids_limit: 64
    stop_grace_period: 15s
    logging:
      driver: local
      options:
        max-size: "5m"
        max-file: "2"

volumes:
  ros-watch-data:
```

部署成功后访问 `http://<1Panel服务器内网地址>:8086` 创建管理员。若端口已占用，只修改 `ports` 冒号左侧的宿主机端口；修改日志端口后，RouterOS 接入时也填写修改后的端口。数据保存在 `ros-watch-data` 命名卷中，升级时保持编排名称和数据卷不变，不要选择删除数据卷。


## Docker 命令部署

不使用 Compose 时：

```sh
docker volume create ros-watch-data
docker run -d --name ros-watch --restart unless-stopped --init \
  -p 8086:8080 -p 5514:5514/udp \
  -v ros-watch-data:/data --read-only --cap-drop ALL \
  --security-opt no-new-privileges:true --memory 192m --cpus 0.5 --pids-limit 64 \
  --log-driver local --log-opt max-size=5m --log-opt max-file=2 \
  ghcr.io/miwyf/ros-watch:latest
docker inspect --format '{{.State.Health.Status}}' ros-watch
```

命名卷保存账号、设置和日志，删除或重建容器时保留它。Docker 命令方式与 Compose 默认生成的卷名不同，迁移已有部署时必须使用原来的数据卷。

## 从源码编译部署

需要 Go **1.27.3 或更新版本**。以下命令用于 Linux；默认网页端口为 8080：

```sh
git clone https://github.com/miwyf/ros-watch.git
cd ros-watch
go mod download
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ros-watch .
./ros-watch
```

保持程序运行，在浏览器访问 `http://<服务器内网地址>:8080`。程序默认在当前目录的 `data/` 保存数据库；长期运行可交给 systemd 等服务管理器，服务工作目录设为项目目录，使用普通用户且保证数据目录可写。迁移编译产物时同时复制 `geo/` 到工作目录；网页和 RouterOS 模板已嵌入程序。

也可用 Docker 本地编译，沿用相同端口、数据卷和资源限制：

```sh
docker compose -f compose.yaml -f compose.build.yaml up -d --build
```

仅构建镜像时使用 `docker build --target production -t ros-watch:local .`。通过本地构建部署后，更新也使用上述两个 Compose 文件。

## 首次使用

默认发布网页 TCP 8086、syslog UDP 5514；需要改端口时修改 `compose.yaml` 的 `ports`。将其部署在可信内网，宿主机防火墙应限制日志来源。

首次访问 `http://<服务器内网地址>:8086`，网页自动进入 **创建管理员** 页面。填写用户名、密码和确认密码，提交后直接进入看板。用户名默认预填 `admin`，可以修改；密码由你设置，8–128 位。

创建完成后初始化入口关闭，再次访问显示正常登录页。已有账号的部署升级后沿用原账号、密码、设置和日志，不会重新开放初始化。账号创建具备表单 CSRF 校验；并发提交只允许一个管理员创建成功。

进入 **系统设置**：

1. 在“账号安全”修改用户名或密码。修改时必须验证当前密码，修改后所有旧登录失效。
2. 在“日志与观察规则”填写允许发送日志的路由器地址和 WAN 接口映射。
3. 设置保留天数、队列、接收限速、扫描阈值及 ROS 采样情况，保存后生效。白名单未配置时拒绝所有日志来源。

数据库仅存储 Argon2id 密码哈希（独立随机盐，19 MiB / 2 次迭代 / 1 并行度），不保存可取回的明文密码。账号和设置在 `/data/control.sqlite3`，日志在 `/data/events.sqlite3`，会话密钥在 `/data/session.key`，全部位于持久命名卷。改变环境变量不会覆盖后台已保存的账号或参数。

容器以非 root 运行，只读根文件系统，仅 `/data` 可写，默认限制 192 MiB 内存 / 0.5 CPU。不要执行 `docker compose down -v`，它会删除账号、设置和日志所在的数据卷。

## 后台可修改的参数

| 项目           |                           默认值 | 范围 / 说明                                    |
| -------------- | -------------------------------: | ---------------------------------------------- |
| 日志来源白名单 |                               空 | 支持 IP/CIDR，空表示暂停接收，禁止全部地址网段 |
| WAN 映射       | pppoe-out1=WAN1、pppoe-out2=WAN2 | 名称调整只影响后续记录                         |
| 最长保留       |                             7 天 | 1–31 天；空间不足时按所选策略处理              |
| 最低可用空间 | 1024 MB | 低于此值触发磁盘不足处理 |
| 磁盘不足处理 | 自动清理最旧日志 | 可改为暂停写入 |
| 接收处理上限   |                        500 条/秒 | 1–5000，超出计入处理限速丢弃                   |
| 等待队列       |                          4096 条 | 1–8192，调整容量时保留已排队记录               |
| 扫描端口阈值   |                               15 | 2–1000 个不同端口                              |
| 扫描窗口       |                           300 秒 | 60–3600 秒，固定窗口                           |
| ROS 端采样情况 |                           未确认 | 只标记统计口径，不修改 ROS                     |

日志保留只需填写天数。后台约每 30 秒清理原始日志和访问记录中过期的记录，不再按条数或数据库预算提前清理。降低天数会删除新期限之外的旧记录，无法自动恢复。

设置页和接收状态显示只读的数据库实际占用（包含 WAL/SHM）、磁盘可用空间、记录总数、最早及最新记录时间。数据库大小是已用空间，不是需要填写的预算。删除记录后 SQLite 可能保留文件内空闲页用于复用，文件不会一定立即缩小。

最低可用磁盘空间默认 1024 MB，已有部署保留原值。低于该阈值时，默认自动清理最旧日志，可能提前删除保留期内的记录；也可选择“暂停写入，保留现有日志”。页面显示累计空间清理条数和最近清理时间，实际可查范围始终可见。自动清理仍无法恢复到阈值或无法读取磁盘状态时暂停写入，恢复空间后继续；暂停期间的新日志可能丢失。自动清理分批执行，无法通过清理日志恢复阈值时会保留剩余历史并提示释放其他磁盘空间。保留天数不保证从未接收的历史记录存在，实际可查范围在页面显示。

升级会自动移除旧配置中的 max_rows、max_db_mb，保留账号、会话、最低可用空间和其他采集参数；新增磁盘不足处理方式默认自动清理。旧版网页需刷新后再保存设置。回滚到 0.5.x 时需同时恢复升级前的 control.sqlite3，旧版不支持简化后的设置格式。日志库无需迁移或清空。

容器自身日志继续独立轮转（5 MiB × 2）。原文与解析后的访问记录同时保存，但仅按保留天数管理；旧版本未保存的系统日志无法补回。

部署参数 `DATA_DIR`、`HTTP_HOST/PORT`、`SYSLOG_HOST/PORT`、`COOKIE_SECURE`、`DEMO_MODE` 可按需通过 Compose 覆盖。HTTPS 由反向代理提供时，确认已通过 HTTPS 访问后再设置 `COOKIE_SECURE=true`；HTTP 保持 false。

## 更新已有部署

保留同一命名卷和 Compose 项目名。Go 程序直接读取已有 SQLite 账号、设置和日志，以及标准 Argon2id 密码哈希。更新前备份数据和部署文件，再用新镜像重建容器；账号和后台参数不变；若登录过期，重新登录即可。

## 忘记密码

在服务器的项目目录执行交互式恢复：

```sh
docker compose exec ros-watch /ros-watch reset-password
```

命令通过隐藏输入读取新密码，不接受命令行密码参数。需要同时恢复用户名时追加 `--username 新用户名`。恢复会让所有旧网页登录失效，不删除日志。

## 系统日志与登录审计

侧边栏“系统日志”保存可信来源的原始 syslog，支持时间、设备名称、类别、级别、账号、登录来源、事件和关键词筛选、分页、原文展开以及当前筛选最新 10000 条 CSV 导出。普通未知日志也保留，原文上限沿用单个 UDP 报文 4096 字节，非法 UTF-8 用替代字符表示。显示时间以服务器接收时间为准，同时保留设备原始时间，不推断 BSD 时间中缺少的年份。

分类包括登录、基础配置变更、接口/拨号、脚本、系统、防火墙和其他。登录解析账号、明确提供的来源及方式；没有来源的 API 配对日志不猜测来源。REST/API 配对原文分别保留，成功条数不等于独立登录会话数。同一来源同一账号在 UTC 对齐的 5 分钟固定窗口至少 5 条失败时显示连续失败提示；只是提示，不发送外部通知、不自动封禁。配置变更只展示日志记录的事件，不提供完整配置差异，也不判断是否入侵。

请在“RouterOS 接入”生成“已有接入：补充系统日志”指令。它新增独立 ROSWatchSystem syslog 动作，沿用已有 ROSWatch 的目的地址、UDP 端口和发送源地址；按类别添加互斥规则并使用 severity=auto，默认排除 debug/packet。现有防火墙转发和放行/拦截策略保持不变。页面提供检查、完整撤销、仅撤销系统日志三种辅助指令。新接入完整脚本也包含系统日志部分。

UDP 可能丢包，来源白名单只限制发送地址而不构成密码学认证。原文可能包含账号、网络配置和脚本消息，请在可信内网使用并按现有保留策略管理。主动查询、自动封禁、精确配置对比、漏洞识别、洪泛计数采集不在本版本范围内。

## 接入 RouterOS

侧边栏独立的「RouterOS 接入」页面提供脚本生成和复制，可通过 `/#routeros` 直接进入，刷新后保留该页面；系统设置仅管理账号和采集参数。填写 ROS 可访问的日志服务器 IPv4 地址与 Docker 对外映射的 UDP 端口（默认 5514），生成后在 WinBox 的 New Terminal 中手动执行。网页地址为非回环 IPv4 时预填该地址；使用域名或反向代理时需要手动确认日志服务器地址。这里的输入仅用于生成指令，不更改看板监听地址或采集设置。

接入、检查、撤销指令分开显示和复制。已接入的路由器使用补充系统日志指令；完整模板会阻止重复添加 ROSWatch。需先在后台保存日志来源白名单，并核对 ROS 的 WAN 接口列表和现有防火墙规则。

将 ROS 防火墙 syslog 发到服务器 UDP 5514。记录应包含 `input:` / `forward:`、入站接口、协议和 `来源->目标`；普通系统日志会保存在系统日志页，不参与外网访问统计。

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

## IP 归属地

来源 IP 排行、疑似扫描和访问记录在 IP 下方显示国家、省市及可查到的运营商。浏览器会将国家名称显示为中文；其他地区名称使用数据库原文。CSV 导出末尾增加 `src_location`、`src_isp` 两列。

使用镜像内置的 ip2region IPv4 / IPv6 离线数据库，归属地查询在本地完成。只读数据约 46 MiB，索引约 1 MiB，查询结果缓存最多 4096 条；不在接收/写库流程查询，已有日志无需重新入库即可显示归属地。数据库不可用时日志采集继续运行，页面会显示归属地库暂不可用。

归属地仅表示数据库记录的网络出口地区，可能过时或有误，不代表访问者实际物理位置或身份。内网、共享地址和文档地址单独标记。数据版本、来源、SHA256、许可和更新方法见 [geo/SOURCE.md](geo/SOURCE.md)。

## 更新与备份

默认 `latest` 的部署执行以下命令更新镜像并重建容器，数据卷保留：

```sh
docker compose pull && docker compose up -d
```

1Panel 在编排中拉取最新镜像后重新部署。固定版本的部署需先修改镜像标签；从源码本地构建的部署沿用前述两个 Compose 文件和 `--build` 命令。备份包含完整持久卷中的账号库、日志库、会话密钥及需要的部署配置。可停服务后备份卷；运行时请使用 SQLite 备份 API 导出一致性副本，不要只复制正在写入的单个日志库而忽略 WAL。含密码哈希、日志地址和会话密钥的备份也应按私人数据保护。

## GitHub 自动构建镜像

[GitHub Actions](https://github.com/miwyf/ros-watch/actions/workflows/container.yaml) 在推送 `main`、推送 `v*` 标签、提交 PR 或手动运行时执行测试、竞争检测、静态检查及 Compose 校验。测试通过后，main / 版本标签会自动构建并推送 amd64、arm64 镜像到 GHCR，并启动已发布镜像做健康和初始化页面检查；PR 只测试，不发布。

- `main` 发布 `latest` 和 `sha-<完整提交号>`。
- `v0.6.1` 这类标签发布 `0.6.1` 和对应 SHA 标签，标签版本必须与 `config.go` 一致。
- 工作流使用仓库自带 `GITHUB_TOKEN` 发布到 GHCR。首次发布后需在 GitHub 的 Package settings 将镜像包可见性设为 Public，公开仓库本身不会自动公开镜像。
- 新版本修改 `config.go`、README 和 Compose 的版本号，推送 main 并确认检查通过，再推送同版本的 `v` 标签。发布标签不应移动或覆盖。

## 开发验证

Go 1.27.3+：

```sh
go test -count=1 ./...
go vet ./...
go build -o ros-watch .
docker build --target test -t ros-watch-tests .
```

Docker 测试阶段还运行竞争检测；测试工具不进入生产镜像。使用项目外的临时数据目录运行 `ros-watch`；首次打开网页创建管理员，账号库仅保存密码哈希。设置 `DEMO_MODE=true` 和 `HTTP_HOST=127.0.0.1` 可生成文档地址演示数据（必须绑定回环），不要把演示库接入生产。

`go run ./cmd/probe` 提供 1–60 秒的有限合成 UDP/HTTP 验证，仅用于隔离测试实例。该工具要求全新、未初始化的隔离实例，自行创建测试账号并把日志白名单改为回环地址；不可对生产服务运行。Linux 下可采样进程 RSS 和单核 CPU 使用率；CPU 采样按 Linux USER_HZ=100 换算。

## 官方资料

- [RouterOS 日志](https://manual.mikrotik.com/docs/diagnostics-monitoring-and-troubleshooting/log/)
- [Go net/http](https://pkg.go.dev/net/http)
- [OWASP 密码存储](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html)
- [Docker 环境变量与 Secrets](https://docs.docker.com/compose/how-tos/environment-variables/best-practices/)
