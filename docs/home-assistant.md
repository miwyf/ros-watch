# Home Assistant MQTT 接入

ROS Watch 通过 MQTT Discovery 自动创建一个设备和 11 个只读实体。需要已有的 MQTT Broker 和 HA MQTT 集成；无需在 RouterOS 增加 MQTT 配置。

## 配置

在“系统设置 → Home Assistant”中填写：

- Broker：`tcp://主机:1883`；TLS 使用 `ssl://证书对应主机:8883`。也接受 `mqtt://` / `mqtts://`。
- 用户名和密码：与 Broker 的访问权限一致。密码保存后不回传，留空保留，勾选清除才删除。
- 设备名称：默认 `ROS Watch`。
- 发现前缀：默认 `homeassistant`，须与 HA MQTT 集成一致。

启用后保存。连接和发布在后台进行；“立即发布”重新发送发现配置及当前数据。成功提示表示 Broker 已确认接收，还需在 HA 的“设置 → 设备与服务 → MQTT”中确认设备和实体。

MQTT 设置、密码与设备 ID 保存在持久数据卷的 `control.sqlite3` 中。密码不是哈希，因为连接 Broker 时必须读取原值；保护数据卷和备份。TLS 验证系统信任链，不提供忽略证书校验选项。

## 实体与口径

| 实体 | 口径 |
| --- | --- |
| 访问记录、独立来源、目标端口 | 最近 12 小时，所有已配置 WAN，沿用访问概览的统计口径 |
| 疑似扫描来源 | 沿用固定窗口及阈值；达到现有结果上限时显示 `20+`，不是完整精确计数 |
| 每分钟访问记录 | 上一个完整自然分钟的访问记录数，时间依据服务器接收时间；HA 保存接入后的历史 |
| 日志接收状态 | 正在接收、等待日志、接收或存储异常；演示模式明确显示演示数据 |
| 最后收到日志 | 最近有效日志的接收时间；可能是系统日志或防火墙日志，无日志则未知 |
| 统计更新时间 | 本轮查询使用的时间截止点，以 UTC ISO 8601 发布，HA 按本地时区显示 |
| 来源排行 | 来源前 5，属性 `items` 包含来源 IP、记录数、端口数和归属地 |
| 端口排行 | 端口 / 协议前 5，属性 `items` 包含端口、协议和记录数 |
| 接收与存储异常 | 接收服务错误或磁盘空间状态异常；不代表网络安全评级 |

访问记录反映连接尝试，不代表成功连接、攻击成功或已封禁。归属地代表出口地区，不是个人身份或实际位置。每分钟趋势不是 12 小时滚动总量，不会自动回填 HA 接入前的历史。

窗口指标的实体 ID、MQTT 状态字段和看板引用统一使用 `_12h`，并携带 `window_hours: 12`。为保持设备身份，首次接入生成的 unique_id 与 Discovery topic 仍可能包含旧 `_6h` 后缀，仅为内部兼容标识。已有 HA 安装可在实体设置中将这 4 个实体 ID 改为 `_12h`，再使用新看板；每分钟曲线的实体 ID 保持不变。

## 发布与恢复

- 固定每 60 秒计算并发布一轮，额外窄范围统计上一分钟。默认关闭；没有新增预聚合表或独立桥接服务。
- MQTT 3.1.1，QoS 1；发现、最新状态及排行属性使用保留消息。
- Topic：`ros_watch/<持久实例ID>/state`、`availability`、`top_sources`、`top_ports`。
- Discovery：`<发现前缀>/<sensor或binary_sensor>/ros_watch_<实例ID>_<实体键>/config`。
- 订阅 `<发现前缀>/status`，收到 `online` 后补发发现和状态。若 HA 自定义了 birth topic，应保留此默认主题或另行触发“立即发布”。
- Broker 断线后每 15 秒重试。LWT 和正常退出发布 `offline`；统计失败不发布零值，标记不可用。实体 180 秒无状态更新也会过期。
- MQTT 和数据库查询均有超时。统计使用现有查询并发额度，接收/写入路径不等待 MQTT。12 小时数据过多时仍可能查询失败；接入功能不会改变现有查询能力。
- 关闭接入保留实体及历史，但置为不可用；重新开启恢复原设备和实体。修改 Broker 或发现前缀后，旧配置可能需要在旧 Broker / HA 中手动移除。
- 实例 ID 自动生成并持久化；不要让两个同时运行的实例共用复制的数据卷，否则会争用同一 MQTT 客户端 ID。
- 所有实体仅展示，不创建控制命令。发送汇总数字、排行和状态，不发送原始日志。

## HA 看板

点击“下载 HA 看板”获取当前深色大屏版，在 HA 创建独立看板，将下载的 YAML 粘贴到原始配置编辑器。大屏需要 button-card、apexcharts-card 和 vertical-stack-in-card。没有这些组件时，点击“下载简版”，仅使用原生卡片，无 HACS 依赖。

导出使用发现配置中声明的默认实体 ID。如果用户在 HA 改过实体 ID，或 HA 因重名分配了其他 ID，需要更新 YAML 引用。首次接入时曲线需要等待 HA 累积数据。

## 可选的大屏样式

[网络边界大屏模板](home-assistant-observatory.yaml) 提供暗色背景、大数字、访问曲线和带条形图的排行。需要已安装并注册 `button-card`、`apexcharts-card`、`vertical-stack-in-card`，“下载简版”没有这些依赖。

使用前，把模板里的 `INSTANCE` 全部替换为接入设置接口返回的 `instance_id`，确认实体 ID 与 HA 中一致，再导入独立看板。模板只调整该看板，不修改 HA 全局主题。顶部统计最近 12 小时，曲线展示最近 12 小时、读取 HA 已记录的分钟数据；演示和不可用状态会明确显示。

## 开发验证

运行 `go test ./...`、`go vet ./...`、`node --test scripts/test_*.cjs`。

设置 `ROS_WATCH_MQTT_TEST_BROKER=tcp://隔离测试Broker:1883` 后，`TestMQTTBrokerLifecycle` 会使用真实 Broker 验证发现、保留状态、HA birth、查询失败恢复、LWT/重连及关闭。请勿对生产 Broker 运行此测试：它会发布独立的测试设备，并发送 HA birth 消息。

参考：[HA MQTT Discovery](https://www.home-assistant.io/integrations/mqtt/)、[MQTT Sensor](https://www.home-assistant.io/integrations/sensor.mqtt/)、[Paho MQTT Go](https://github.com/eclipse-paho/paho.mqtt.golang)。
