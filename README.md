# OpenSurge for Linux

OpenSurge for Linux 是 [OpenSurge for Mac](https://github.com/YTwsy/OpenSurge-for-Mac)
的 fork，目标是将 OpenSurge 的完整功能移植到 Linux，并按 Linux 的网络栈和服务
边界重新实现。它是一个面向 Linux 的 Surge 风格家庭网关控制面。当前仓库建立
配置契约、mihomo 配置渲染和完整的 Linux 网关生命周期：dnsmasq 提供
DHCP/DNS，mihomo TUN 提供透明代理，nftables 提供 OpenSurge 专属的转发/NAT
规则，systemd 负责已安装服务的边界。

## 支持范围

- Debian 12+、Ubuntu 22.04+。
- amd64、arm64。
- IPv4 是当前网关数据面的支持协议。
- 透明代理唯一使用 mihomo TUN；`mihomo.redir_port` 必须保持为 `0`。
- 防火墙只允许 OpenSurge 操作自己的 `inet opensurge` nftables table，不会
  清空系统全局规则。

## 网络模式

| 模式 | 用途 | 约束 |
| --- | --- | --- |
| `isolated_lan` | 独立下游网络 | 需要第二块有线网卡或 VLAN；OpenSurge 提供下游 IPv4、DHCP、DNS 和 NAT。 |
| `same_lan` | 同一局域网旁路由 | 上下游使用同一接口，DHCP 必须关闭；只对明确配置的 IPv4 路径负责。 |
| `same_wifi_dhcp` | 已确认停用上游 DHCP 的共享接口 | 必须显式确认路由器 DHCP 已关闭；停止后不会替用户重新开启路由器 DHCP。 |

`isolated_lan` 不提供下游 IPv6 配置并丢弃下游 IPv6 forwarding。其余模式
会提示未托管的 IPv6 路径，不把 IPv6 绕行误报为已验证。

### 发布安装器的初始拓扑

首次运行发布安装器时，它只会根据 IPv4 默认路由的精确 Linux 接口名及其源 IPv4
生成 `same_lan` 控制平面配置：上下游接口相同，OpenSurge DHCP 和透明代理均关闭。
这不会把任意单网卡主机转换为隔离 LAN 网关，也不会把 `lan0` 或 `wan0` 之类的示例
名称映射到本机接口。

要使用 `isolated_lan`，必须显式提供已经存在的下游接口（或已创建的 VLAN）、上游
接口、已配置在下游接口上的 LAN IPv4，以及 LAN CIDR。安装器不会创建 VLAN、添加
地址，或推断上游。CIDR 不是 `/24` 时，还必须显式提供位于该 CIDR 内且顺序正确的
DHCP 起止地址。

Linux 的 mihomo TUN 始终启用 `auto-route` 和 `auto-redirect`。为避免部分
nftables/netlink 环境返回 `EEXIST`，`route-exclude-address` **不得**加入
`255.255.255.255/32`。因此有限广播发现（例如向该地址发送的服务发现）不在当前
已验证的数据面契约内。

## CLI

```sh
go run ./cmd/opensurge config validate --config examples/config.example.yaml
go run ./cmd/opensurge config migrate --config /path/to/old-config.yaml > candidate.yaml
go run ./cmd/opensurge doctor --config examples/config.example.yaml
```

`config migrate` 只读取源文件，把候选 YAML 写到 stdout，把需要人工确认的
映射写到 stderr；它不会写入或覆盖任何文件。迁移后必须人工映射下游接口、
上游接口和管理监听 IPv4 地址，再运行 `config validate`。迁移不会改变上游
路由器的 DHCP 状态。

默认配置路径是 `/etc/opensurge/config.yaml`。默认运行数据目录为
`/var/lib/opensurge`，运行时 socket 方向为 `/run/opensurge`。

## 安装与管理

唯一受支持的 Debian/Ubuntu 安装入口是 GitHub Release 中的发布安装器。它会下载与
当前架构匹配的 `.deb`、用同一 Release 的 `SHA256SUMS` 验证它、自动安装所需依赖，
再完成首次配置、管理员初始化和控制面启动。安装器必须能写入控制 TTY，才能只向该
终端显示一次性管理员密码；不支持无人值守或没有可写控制终端的安装。

也可以直接通过 `curl` 一键下载并执行最新版本的安装器：

```sh
curl -fsSL https://github.com/three-b0dy/OpenSurge-for-Linux/releases/latest/download/opensurge-install | sudo bash
```

```sh
curl -fLO https://github.com/three-b0dy/OpenSurge-for-Linux/releases/latest/download/opensurge-install
sudo bash ./opensurge-install
```

默认命令选择最新 Release。若需固定版本，仍使用相同的安装器并传入 tag：

```sh
sudo bash ./opensurge-install --version vX.Y.Z
```

离线介质必须同时包含 `opensurge-install`、匹配架构的
`opensurge_<version>_$(dpkg --print-architecture).deb` 和同目录的 `SHA256SUMS`；
安装器不会为本地包生成校验和，也没有跳过校验的选项。校验文件位于其他位置时可明确
指定：

```sh
sudo bash ./opensurge-install \
  --deb ./opensurge_<version>_$(dpkg --print-architecture).deb \
  --checksums /media/opensurge/SHA256SUMS
```

不要执行 `dpkg -i` 或 `apt install ./…deb`，包括升级场景。`.deb` 的 `preinst` 会
拒绝这些直接安装路径；这是为了确保依赖、DNS 交接、端口检查和回滚都由同一受控事务
执行。

首次成功安装会在控制 TTY 显示 `admin` 的一次性密码，并建立有效期十年的 RSA-3072
自签名证书。登录 Web GUI 后立即修改密码；密码不会写入安装器日志、命令参数或环境变量。
忘记密码时，可在有控制 TTY 的主机上执行：

```sh
sudo opensurge-setup reset-password
```

首次默认配置是安全的 `same_lan` 控制平面：安装器使用 IPv4 默认路由的精确接口名和
源 IPv4，把同一接口写入 `gateway.interface` 与 `gateway.upstream_interface`，并保持
DHCP 和透明代理关闭。已有 `/etc/opensurge/config.yaml` 与已有管理员状态不会被覆盖。
`eth0`、`ens18`、`enp1s0.50`、bridge 和 VLAN 等实际 Linux 链路名均直接使用；
`lan0`、`wan0` 只是示例，不是别名。

要首次部署隔离 LAN，先由管理员创建 VLAN/配置下游地址，再显式提供真实拓扑。例如：

```sh
sudo bash ./opensurge-install --mode isolated_lan \
  --downstream-interface enp2s0.50 \
  --upstream-interface enp1s0 \
  --lan-ip 192.168.50.1 \
  --lan-cidr 192.168.50.0/24
```

非 `/24` 的隔离 LAN 还必须提供 CIDR 内、按升序排列的
`--dhcp-range-start` 和 `--dhcp-range-end`。安装器不创建 VLAN、不添加地址，也不猜测
上游接口；首次 `same_wifi_dhcp` 配置同样不能由默认安装流程推断。

`opensurge-gateway.service` 以 root 身份拥有网络数据面；
`opensurge-control.service` 以受限的 `opensurge` 用户提供配置中
`management.listen` 指定 LAN IPv4 上的 HTTPS Control API 与 Web GUI。浏览器对首次
自签名证书的告警是预期行为。之后可将经校验、且含该监听 IP SAN 的自有证书和私钥放入
`/etc/opensurge/tls/`，并执行：

```sh
sudo opensurge-setup replace-certificate \
  --cert /etc/opensurge/tls/custom-cert.pem \
  --key /etc/opensurge/tls/custom-key.pem
```

登录采用单管理员的普通路由器式会话登录。

### DNS、服务与故障恢复

安装器先保留主机现有 resolver 的形式。仅当 `systemd-resolved.service` 正在运行时，
它才会优先选择安装前 `/etc/resolv.conf` 中第一个有效、非本地的 IPv4/IPv6
`nameserver`；没有时才使用 IPv4 默认路由的 `via` 网关。两者都不可用会在依赖安装和
网络修改之前停止。获得安全上游后，安装器被授权 `disable --now systemd-resolved.service`
并以常规文件替换 `/etc/resolv.conf`。

为避免与 OpenSurge 自己的 DNS 角色冲突，安装器会记录通用 `dnsmasq.service` 的原状态，
并在安装依赖后按需 `disable --now` 它。临时 `policy-rc.d` 阻止依赖包自动启动服务；
不会覆盖已有的主机策略。新安装在已停止这些已知服务后检查 TCP 和 UDP 53 端口：任何
未知监听者都会列出协议、地址、PID 和进程名后拒绝安装，安装器绝不会停止或杀死该进程。

安装器将仅限 root 的事务状态保存在 `/var/lib/opensurge/install-state/`，诊断日志在
`/var/log/opensurge-install.log`。失败时，以及后续执行包的 remove 或 purge 时，它只恢复
清单证明由 OpenSurge 改动的 resolver、`systemd-resolved` 和通用 `dnsmasq` 状态。无效的
状态清单会被保留以供人工恢复，不能被自动信任。

## 上游镜像

`upstream` 分支由 GitHub Actions 每日同步，也可通过手动 workflow
dispatch 触发。同步使用受保护的精确 ref lease，只更新 `upstream`，不会
改写 Linux 分支，也不会发布 release。

## 开发验证

```sh
make test
make web-test
make installer-test
make linux-ci-check
make deb ARCH=amd64 VERSION=0.0.0-dev
sudo -v && make linux-lab-test
sudo -v && make linux-lab-test-tun
```

`make test`、`make web-test`、`make installer-test`、`make linux-ci-check` 和 `.deb`
构建分别覆盖单元、Web、安装器 fixture、仓库/包静态契约和打包；成功安装包、配置校验
或 HTTPS Control API 启动也只是 package/config/startup smoke，均不证明下游真实流量。

`make linux-lab-test` 以 Linux network namespaces 验证 DHCP、DNS、NAT、nftables
所有权和回滚清理。`make linux-lab-test-tun` 额外验证无显式客户端代理的 HTTPS 流量
进入 mihomo TUN 日志：DNS 通过 `dig` 单独断言，HTTPS 使用 `curl --resolve` 固定测试地址，
而非依赖 curl 是否支持 `--dns-servers`。两者均需要 Linux root/network namespace 能力，
不能由 macOS 主机测试替代。Orb arm64 打包/离线安装和指定 QA 主机上的 CLI/日志验收是
独立证据门禁；没有各自的实际命令输出时，不应声称它们已经运行。

更多迁移说明见 [docs/linux-migration.md](docs/linux-migration.md)。
