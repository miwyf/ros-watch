# 开发与测试

本文用于修改源码后的验证，部署步骤见 [README](../README.md)。GitHub Actions 自动运行测试、竞争检测、静态检查和镜像启动检查。

## 本机验证

需要 Go 1.27.3+ 和支持竞争检测的 C 编译工具链。在项目根目录执行：

```sh
go test -race -count=1 ./...
go vet ./...
```

程序构建与运行见 [从源码编译部署](../README.md#从源码编译部署)。

## 容器验证

也可使用 Dockerfile 的 `test` 阶段完成测试、静态检查和竞争检测。在项目根目录执行：

```sh
docker build --target test -t ros-watch-tests .
```

这是本机验证的另一种方式，测试工具保留在测试镜像中。生产镜像使用独立的 `production` 阶段。

验证容器运行用户及数据卷迁移需要 Docker、Python 3 和 `alpine:3.21`：

```sh
docker build --target build -t ros-watch:uidgid-build .
docker build --target production -t ros-watch:uidgid-local .
python3 scripts/test_container_user.py
```

脚本使用 Docker 宿主机 `/tmp` 下的独立临时目录和合成账号、日志，完成后删除本次测试容器与数据。覆盖默认/空 UID/GID、分别设置、`99:100`、重启、旧用户迁移及错误配置；检查实际进程 UID/GID、权限集和数据保留。可用 `--baseline-image 旧镜像` 验证指定历史版本升级，用 `--report 项目外路径.json` 保存结果。

## 隔离实例与探针

开发实例使用项目外的临时数据目录，通过 `DATA_DIR` 指定。首次打开网页创建管理员；设置 `DEMO_MODE=true` 和 `HTTP_HOST=127.0.0.1` 可生成演示数据，演示模式必须绑定回环地址。

`go run ./cmd/probe` 提供 1–60 秒的合成 UDP/HTTP 验证。它要求全新、未初始化的隔离实例，会创建测试账号并把日志白名单改为回环地址，因此只能用于测试实例。通过 `-url`、`-udp`、`-seconds`、`-rate` 指定目标和负载；参数说明可用 `go run ./cmd/probe -h` 查看。

Linux 下探针可采样进程 RSS 和单核 CPU 使用率；CPU 采样按 Linux USER_HZ=100 换算。

## 参考资料

- [Go net/http](https://pkg.go.dev/net/http)
- [OWASP 密码存储](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html)
