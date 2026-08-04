# OpenSurge for Linux

OpenSurge 是一个面向 Linux 的 Surge 风格家庭网关控制面。当前仓库建立
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

GitHub Release 提供 amd64 与 arm64 的 `.deb`。在匹配架构的 Debian/Ubuntu 主机上
安装后，先检查并编辑 `/etc/opensurge/config.yaml`，再从本机 TTY 初始化管理员：

```sh
sudo dpkg -i opensurge_<version>_$(dpkg --print-architecture).deb
sudo opensurge-setup init --username admin
sudo systemctl enable --now opensurge-gateway.socket opensurge-control.service
```

`opensurge-gateway.service` 以 root 身份拥有网络数据面；
`opensurge-control.service` 以受限的 `opensurge` 用户提供配置中
`management.listen` 指定 LAN IPv4 上的 HTTPS Control API 与 Web GUI。首次初始化会
生成有效期十年的 RSA-3072 自签名证书；浏览器告警是预期行为。之后可将经校验、且含该
监听 IP SAN 的自有证书和私钥放入 `/etc/opensurge/tls/`，并执行：

```sh
sudo opensurge-setup replace-certificate \
  --cert /etc/opensurge/tls/custom-cert.pem \
  --key /etc/opensurge/tls/custom-key.pem
```

登录采用单管理员的普通路由器式会话登录。忘记密码时在本机 TTY 执行
`sudo opensurge-setup reset-password --username admin`。

## 上游镜像

`upstream` 分支由 GitHub Actions 每日同步，也可通过手动 workflow
dispatch 触发。同步使用受保护的精确 ref lease，只更新 `upstream`，不会
改写 Linux 分支，也不会发布 release。

## 开发验证

```sh
make test
make web-test
make build
sudo -v && make linux-lab-test
sudo -v && make linux-lab-test-tun
```

`make linux-lab-test` 以 Linux network namespaces 验证 DHCP、DNS、NAT、nftables
所有权和回滚清理。`make linux-lab-test-tun` 额外验证无显式客户端代理的 HTTPS 流量
进入 mihomo TUN 日志。两者均需要 Linux root/network namespace 能力，不能由 macOS
主机测试替代。

更多迁移说明见 [docs/linux-migration.md](docs/linux-migration.md)。
