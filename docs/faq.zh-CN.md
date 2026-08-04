# OpenSurge for Linux 常见问题

## 支持哪些平台？

目标平台是 Debian 12+ 或 Ubuntu 22.04+，架构为 amd64 或 arm64。GitHub Release 提供
对应架构的 `.deb`，其中包含可安装的 Linux 网关和 LAN 控制面。

## 应该选择哪种网络模式？

有第二块有线网卡或 VLAN 时使用 `isolated_lan`。共享接口且关闭 DHCP 时使用 `same_lan`。
只有在确认上游路由器 DHCP 已关闭后，才使用 `same_wifi_dhcp`。迁移不会替用户改变上游
路由器的 DHCP 状态。

## 透明代理如何工作？

唯一支持的透明路径是 mihomo TUN 自动路由/重定向，`redir_port` 必须保持为零。不要在
`route-exclude-address` 中加入 `255.255.255.255/32`：它可能令 Linux auto-redirect
初始化以 `EEXIST` 失败。有限广播服务发现目前不属于已验证的数据面路径。

## 迁移会直接应用配置吗？

不会。`config migrate` 把候选 YAML 写到 stdout，把映射提示写到 stderr，绝不写文件。
必须人工映射接口和管理监听地址，再运行 `config validate`。

## 如何安装和访问网关？

安装匹配架构的 Release `.deb`，检查 `/etc/opensurge/config.yaml` 后，在本机 TTY 执行
`sudo opensurge-setup init --username admin`，再启用 `opensurge-gateway.socket` 与
`opensurge-control.service`。控制面只在配置的 LAN `management.listen` 地址上提供 HTTPS，
采用单管理员登录。初始化会生成有效期十年的自签名证书；之后可用
`opensurge-setup replace-certificate --cert ... --key ...` 替换为 SAN 包含监听 IP 的证书。
