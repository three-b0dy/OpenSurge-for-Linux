# OpenSurge for Linux 使用拓扑

OpenSurge 面向 Debian 12+、Ubuntu 22.04+ 的 amd64/arm64 主机。当前支持三种配置模式，
并提供 DHCP、DNS、NAT、mihomo TUN 与回滚的 Linux 网关生命周期。

## 三种模式

| 模式 | 用途 | 关键约束 |
| --- | --- | --- |
| `isolated_lan` | 独立下游 LAN | 需要第二块有线网卡或 VLAN；下游 IPv4 由网关配置，IPv6 forwarding 丢弃。 |
| `same_lan` | 同一 LAN 的旁路由 | 使用共享接口；OpenSurge DHCP 必须关闭。 |
| `same_wifi_dhcp` | 已确认上游 DHCP 停止后的共享接口 | 必须显式确认上游路由器 DHCP 已关闭；停止时不会自动恢复该外部服务。 |

IPv4 是当前支持的网关协议。透明代理唯一使用 mihomo TUN 自动路由/重定向；
`redir_port`、REDIRECT 和 TPROXY 不是迁移目标。其他模式不会把未管理的 IPv6 路径
误报为已验证。

Linux TUN 的 `auto-route` 与 `auto-redirect` 下，不能将 `255.255.255.255/32` 放入
`route-exclude-address`；它可能导致 nftables/netlink 初始化返回 `EEXIST`。因此有限广播
服务发现目前没有通过网关数据面验证。

## 迁移与人工映射

```sh
opensurge config migrate --config /path/to/source.yaml > candidate.yaml
opensurge config validate --config candidate.yaml
```

迁移命令不会写入任何文件，也不会改变上游路由器 DHCP。使用候选配置前，必须人工映射
下游接口、承载上游路由的接口、非回环管理监听 IPv4，以及 TUN device。`isolated_lan`
必须使用独立的有线接口或 VLAN，不能把普通单接口网络误当作隔离下游。

## Linux 基础设施

iproute2 负责接口、地址、路由和邻居检查；sysctl 管理 IPv4 forwarding；dnsmasq 提供
需要的 DHCP/DNS；nftables 只负责 OpenSurge 自己的命名表；systemd 管理 root 网关服务和
非特权 LAN HTTPS 控制服务。GitHub Release 提供 `opensurge-install` 和 amd64/arm64 `.deb`；
唯一受支持的安装路径是在有可写控制 TTY 的会话中执行
`sudo bash ./opensurge-install`。安装器会校验包、创建单管理员登录和十年自签名证书，并仅在
该 TTY 显示一次性密码；直接安装 `.deb` 会被拒绝。之后可以用
`opensurge-setup replace-certificate` 替换为自有证书，或在控制 TTY 上用
`opensurge-setup reset-password --username admin` 恢复管理员访问。
