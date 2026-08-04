# OpenSurge for Linux

OpenSurge 是一个面向 Linux 的 Surge 风格家庭网关控制面。当前仓库建立
配置契约、mihomo 配置渲染、Linux 网络适配器和 OpenSurge 专属 nftables
规则；完整网关生命周期与 systemd 单元仍在后续阶段实现。

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

## 开发验证

```sh
make test
make web-test
make build
```

当前代码阶段提供通用 Control API/Web GUI 基础和 Linux 网络原语；Debian
软件包、已安装的 systemd gateway 服务以及生产部署单元尚未实现，不应按可
安装发行版对待。长期方向是以 nftables、iproute2 和 systemd 为 Linux 服务
基础，并在后续阶段接入网关生命周期与 Linux 实验室验证。

更多迁移说明见 [docs/linux-migration.md](docs/linux-migration.md)。
