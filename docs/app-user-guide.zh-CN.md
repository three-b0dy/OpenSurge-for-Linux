# OpenSurge for Linux 使用指南

OpenSurge 是面向 Debian 12+、Ubuntu 22.04+、amd64 和 arm64 的 Linux 网关控制面。
当前阶段提供 CLI、配置校验、候选迁移、Web GUI 基础、mihomo 渲染和 Linux 网络原语；
尚未提供可安装的网关服务包。

## 初始配置

从 `examples/config.example.yaml` 开始，选择一种模式：

- `isolated_lan` 需要第二块有线网卡或 VLAN 作为下游网络。
- `same_lan` 使用共享接口，并要求关闭 OpenSurge DHCP。
- `same_wifi_dhcp` 需要明确确认上游路由器 DHCP 已关闭。

当前网关数据面支持 IPv4。透明代理唯一使用 mihomo TUN，`redir_port` 必须保持为
零。`isolated_lan` 会丢弃下游 IPv6 forwarding，其余模式会提示 IPv6 尚未由本阶段管理。

## 校验与迁移

```sh
opensurge config validate --config /etc/opensurge/config.yaml
opensurge config migrate --config /path/to/source.yaml > candidate.yaml
```

迁移命令只读取源文件，把候选 YAML 写到 stdout，把映射提示写到 stderr；不会写入或覆盖
任何文件，也不会改变上游路由器 DHCP。使用前必须人工映射下游接口、上游接口和非回环
管理监听地址，然后运行配置校验。

## 网络职责

Linux 方向使用 iproute2 检查接口，使用 nftables 管理 OpenSurge 自己的命名防火墙表，
并以 systemd 作为后续服务生命周期方向。当前命令不宣称已经提供 DHCP、DNS、转发、NAT、
回滚或真实主机网络结果；这些能力等待 Linux lifecycle 与实验室门禁实现。
