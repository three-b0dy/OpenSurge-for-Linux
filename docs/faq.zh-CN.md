# OpenSurge for Linux 常见问题

## 支持哪些平台？

目标平台是 Debian 12+ 或 Ubuntu 22.04+，架构为 amd64 或 arm64。当前仓库提供 Linux
基础能力，还没有可安装的完整网关服务。

## 应该选择哪种网络模式？

有第二块有线网卡或 VLAN 时使用 `isolated_lan`。共享接口且关闭 DHCP 时使用 `same_lan`。
只有在确认上游路由器 DHCP 已关闭后，才使用 `same_wifi_dhcp`。迁移不会替用户改变上游
路由器的 DHCP 状态。

## 透明代理如何工作？

当前唯一支持的透明路径是 mihomo TUN 自动路由/重定向，`redir_port` 必须保持为零。

## 迁移会直接应用配置吗？

不会。`config migrate` 把候选 YAML 写到 stdout，把映射提示写到 stderr，绝不写文件。
必须人工映射接口和管理监听地址，再运行 `config validate`。

## 是否已有网关服务包？

还没有。项目正在建设 nftables、iproute2 和 systemd 服务方向，完整 lifecycle 与发行包
属于后续工作。
