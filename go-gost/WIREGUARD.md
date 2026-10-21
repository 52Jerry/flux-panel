# WireGuard 出站转发

本仓库的 `wg` / `wireguard` 节点使用官方 wireguard-go 和用户态网络栈建立
WireGuard UDP 隧道。无需系统 WireGuard 驱动、管理员权限或修改系统路由。
现有 TCP、UDP 端口转发可以通过该节点访问隧道内地址；也可以作为后续代理节点的前置隧道。

## 构建与运行

Go 最低版本为 1.23.1，仓库工具链为 1.23.4。两个 Go 模块均需安装依赖。
在 `go-gost` 目录中执行：

```sh
go build -trimpath -o gost .
./gost -V
./gost -standalone -C examples/wireguard.yaml
```

版本输出应包含 `features=wireguard`。`-standalone` 读取本地 GOST 配置，
不要求面板连接配置 `config.json`，不连接面板或上报配置。
不加该参数时保留原有面板代理模式。

先将 `examples/wireguard.yaml` 中的密钥、WireGuard 服务端地址与目标地址
替换为实际配置。示例把本地 TCP 18080 转发至隧道内 10.77.0.1:8080，
把本地 UDP 15353 转发至隧道内 10.77.0.1:53。
公钥由对端提供，私钥属于本客户端；服务端 peer 的 AllowedIPs 应包含本客户端的隧道地址。

Windows 上可通过 `./build.ps1` 一次构建 Linux amd64、Linux arm64 和 Windows amd64，
产物位于 `dist/`。也可以指定 Go 路径：

```powershell
./build.ps1 -Go C:/path/to/go/bin/go.exe
```

Linux 构建及验证辅助脚本也包含在源码中：

```sh
bash build-linux.sh            # 生成两种架构的程序、测试程序和 SHA256SUMS
bash dist/verify-linux.sh      # 在 Linux 上执行当前架构的协议/端口转发测试
```

PowerShell 构建可使用 `-IncludeLinuxTests` 生成相同的 Linux 测试程序，
使用 `-Targets windows/amd64` 单独构建 Windows，或通过 `-OutputDirectory`、
`-WorkDirectory` 显式指定输出和临时目录。脚本不会修改系统安全设置。
构建 Linux 程序与在 Linux 上运行测试是不同步骤。

## 节点配置

```yaml
name: wg-node
addr: vpn.example.com:51820   # WireGuard 对端的 UDP Endpoint
connector:
  type: wg
dialer:
  type: wg
  metadata:
    privateKey: CLIENT_PRIVATE_KEY_BASE64
    publicKey: SERVER_PUBLIC_KEY_BASE64
    # presharedKey: OPTIONAL_PRESHARED_KEY_BASE64
    address: [10.77.0.2/32, "fd00:77::2/128"]
    allowedIPs: [10.77.0.0/24, "fd00:77::/64"]
    dns: [10.77.0.1]
    mtu: 1420
    keepalive: 25
    dialTimeout: 5s
```

`connector` 和 `dialer` 必须同时使用 `wg`（或别名 `wireguard`）。
裸 `wg://endpoint:port?...` URL 也会选择两者；密钥放在 URL 中时必须 URL 编码，
否则 Base64 中的 `+` 会被解释为空格。推荐使用 YAML/JSON 配置以避免密钥出现在进程参数中。

| 字段 | 含义 |
| --- | --- |
| `privateKey` | 必填，标准 Base64 编码的 32 字节客户端私钥 |
| `publicKey` | 必填，标准 Base64 编码的 32 字节对端公钥 |
| `presharedKey` | 可选，必须与对端一致 |
| `address` | 必填，本地隧道 IP 或 CIDR；保留 CIDR 中的主机地址 |
| `allowedIPs` | 必填，对端可达的 CIDR；按需显式使用 `0.0.0.0/0` / `::/0` |
| `dns` | 可选，隧道内 DNS 服务器 IP；目标为域名时必填 |
| `mtu` | 默认 1420，范围 1280–65455 |
| `keepalive` | 默认 25 秒，0 禁用，最大 65535 秒 |
| `dialTimeout` | 默认 5 秒，限制建链/目标拨号；支持 `5s` 或秒数 |

`address`、`allowedIPs`、`dns` 可使用字符串数组或逗号分隔字符串。
目标域名通过配置的隧道 DNS 查询；未配置 DNS 时拒绝域名目标，不回退到系统 DNS。
WireGuard Endpoint 本身由底层网络解析。若另外在 GOST 的 handler/node 上显式配置了
resolver/hosts，它们仍按 GOST 原有规则优先执行。

同一拨号器的并发连接共享 WireGuard 设备，最后一个连接关闭时设备和 UDP socket 一并关闭。
Keepalive 只在设备有活动连接时运行。转发链的底层 UDP 连接保留原有 interface、socket mark
和前置路由设置；前置代理必须支持 UDP。

当前支持定向 TCP/UDP 转发，不支持 SOCKS5 UDP ASSOCIATE 的空目标多目的地址模式，
也不创建系统级 VPN 网卡。面板 UI 和 Node Manager 暂无 WireGuard 表单；
通过本地 GOST YAML/JSON 配置使用本功能，避免面板更新覆盖手工配置。

## 将修复版安装到 Linux 节点

在仓库根目录使用本地构建产物，避免安装脚本再次下载上游旧二进制：

```sh
sudo env GOST_BINARY=/absolute/path/to/gost-amd64 GOST_REQUIRE_WIREGUARD=1 bash install.sh
```

如果仓库内已存在 `go-gost/dist/gost-amd64` 或 `gost-arm64`，脚本会自动优先使用它。
也支持 `GOST_DOWNLOAD_URL` 指定自行发布的二进制 URL。
`GOST_REQUIRE_WIREGUARD=1` 会在替换现有服务前检查版本标记；下载错误、空文件、
无法运行或缺少 WireGuard 支持的文件都会被拒绝。
未指定本地文件/自定义 URL 且不存在构建产物时，仍保留原有上游下载行为。

## 测试

```sh
cd x
go test ./dialer/wg ./connector/wg ./config/cmd ./chain
go test -race ./dialer/wg ./config/cmd ./chain
cd ..
go test .
```

集成测试使用随机临时密钥，在本机启动独立 wireguard-go 对端与用户态 TCP/UDP 回显服务。
测试检查双栈数据回传、对端握手时间/流量计数、并发连接复用、断开重连、错误公钥/PSK、
超时取消、AllowedIPs、DNS、JSON/YAML 转发链与 socket 释放。
无需外部 VPN 服务；`-race` 在 Windows 上需要可用的 C 编译器。
这些测试不验证实际服务器的 UDP 防火墙、转发/NAT、带宽或跨公网路径。

完整可执行文件的转发测试需要指定刚编译的程序（以下在 `go-gost/x` 中运行）：

```powershell
$env:GOST_TEST_BINARY = (Resolve-Path ../dist/gost-windows-amd64.exe).Path
go test ./dialer/wg -run TestWireGuardStandaloneBinary -v
```

该测试从没有面板 `config.json` 的临时目录启动程序，加载转发配置，
通过本机 TCP/UDP 监听端口访问独立 WireGuard 对端，结束后关闭测试进程。

### 本次本机验证（2026-10-21）

- Go 1.23.12 / Windows amd64：`x` 模块 `go test ./...` 通过；主程序 `go test .` 通过。
- WireGuard、CLI 解析、转发链复制的相关测试连续 10 轮通过。
- Windows 可执行文件实际 TCP/UDP 端口转发通过；`-V` 不再依赖面板配置。
- 修改涉及的包 `go vet` 通过；安装脚本 Bash 语法检查通过。
- `-race` 未能运行：本机缺少 GCC/CGo 编译器。
- Linux amd64、arm64 交叉编译均已通过，ELF64 架构、无动态解释器依赖、
  Go/WireGuard 构建信息和 SHA-256 均已检查；两种架构的 WireGuard 测试程序
  编译及相关包 `go vet` 也已通过。**Linux 测试程序尚未实际运行。**
- 尚未连接实际远端 WireGuard 服务器，也未修改任何线上服务。

构建产物默认在 `dist/`，校验文件为 `SHA256SUMS`，测试程序在 `dist/tests/`。
将完整输出目录复制到 Linux 后，可运行 `bash dist/verify-linux.sh`
检查当前架构程序并执行协议/实际转发集成测试。
这一步不安装系统服务、不修改系统路由，也不要求生产 WireGuard 密钥。

部分安全软件会将 GOST 归类为双用途代理工具，例如 Windows Defender 的
`HackTool:Linux/Gost.B!MTB` 检测。请按所在环境的安全策略审核处理；
构建成功、校验值一致或本机测试通过均不是完整安全审计或“误报”结论。

实现所用 WireGuard API 可参考[官方 netstack 示例](https://git.zx2c4.com/wireguard-go/tree/tun/netstack/examples)。
