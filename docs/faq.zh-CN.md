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

下载 Release 安装器，并在有可写控制 TTY 的会话中运行：

```sh
curl -fLO https://github.com/three-b0dy/OpenSurge-for-Linux/releases/latest/download/opensurge-install
sudo bash ./opensurge-install
```

安装器会校验匹配架构的 `.deb`，在没有已有配置时生成安全的 `same_lan` 控制平面配置，
启动服务，并且只在控制 TTY 显示一次性 `admin` 密码。已有配置和管理员状态会保留。
固定版本使用 `--version vX.Y.Z`；离线介质使用带 `SHA256SUMS` 的
`--deb /path/package.deb`。直接执行 `dpkg -i` 或 `apt install ./package.deb` 会被故意拒绝。
控制面只在配置的 LAN `management.listen` 地址上提供 HTTPS，采用单管理员登录。安装器生成
有效期十年的自签名证书；之后可用 `opensurge-setup replace-certificate --cert ... --key ...`
替换为 SAN 包含监听 IP 的证书。
