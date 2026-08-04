# OpenSurge for Linux 使用指南

OpenSurge 是面向 Debian 12+、Ubuntu 22.04+、amd64 和 arm64 的 Linux 网关控制面。
Release `.deb` 包安装 CLI、root 网关数据面、非特权 LAN HTTPS 控制面、React Web GUI、
systemd 单元以及固定版本的 mihomo。

## 初始配置

从 `examples/config.example.yaml` 开始，选择一种模式：

- `isolated_lan` 需要第二块有线网卡或 VLAN 作为下游网络。
- `same_lan` 使用共享接口，并要求关闭 OpenSurge DHCP。
- `same_wifi_dhcp` 需要明确确认上游路由器 DHCP 已关闭。

当前网关数据面支持 IPv4。透明代理唯一使用 mihomo TUN，`redir_port` 必须保持为
零。`isolated_lan` 会丢弃下游 IPv6 forwarding，其余模式会提示 IPv6 尚未由 OpenSurge 管理。
Linux TUN 配置不得在 `route-exclude-address` 中排除 `255.255.255.255/32`，否则
auto-redirect 的 nftables 初始化可能以 `EEXIST` 失败；有限广播服务发现不属于当前已验证
的数据面路径。

## 安装、初始化与登录

安装匹配架构的 GitHub Release 包，检查配置后，在本机 TTY 初始化：

```sh
sudo dpkg -i opensurge_<version>_$(dpkg --print-architecture).deb
sudo opensurge-setup init --username admin
sudo systemctl enable --now opensurge-gateway.socket opensurge-control.service
```

网关数据面由 root 拥有；控制服务以受限的 `opensurge` 账号运行，只在配置的 LAN
`management.listen` IPv4 地址上提供 HTTPS Control API 与 GUI。初始化会生成有效期十年
的 RSA-3072 自签名证书和管理员账号。确认显示的证书指纹后再接受浏览器告警；也可以替换为
SAN 包含监听 IP 的已校验证书和私钥：

```sh
sudo opensurge-setup replace-certificate \
  --cert /etc/opensurge/tls/custom-cert.pem \
  --key /etc/opensurge/tls/custom-key.pem
```

忘记管理员密码时，在本机 TTY 执行
`sudo opensurge-setup reset-password --username admin`。

## 校验与迁移

```sh
opensurge config validate --config /etc/opensurge/config.yaml
opensurge config migrate --config /path/to/source.yaml > candidate.yaml
```

迁移命令只读取源文件，把候选 YAML 写到 stdout，把映射提示写到 stderr；不会写入或覆盖
任何文件，也不会改变上游路由器 DHCP。使用前必须人工映射下游接口、上游接口和非回环
管理监听地址，然后运行配置校验。

## 网络职责

网关使用 iproute2 检查接口、sysctl 管理 IPv4 forwarding、dnsmasq 提供相应的 DHCP/DNS
角色、nftables 管理 OpenSurge 自己的命名防火墙表，并由 systemd 管理服务生命周期。网关不会
清空全局防火墙规则，停止或回滚时会恢复已记录的 forwarding 状态。使用
`sudo -v && make linux-lab-test` 获得 namespace DHCP/DNS/NAT/回滚证据；使用
`sudo -v && make linux-lab-test-tun` 验证无显式代理 HTTPS 流量进入 mihomo TUN 日志。
