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

唯一受支持的 Debian/Ubuntu 安装入口是 GitHub Release 的发布安装器。它下载与当前架构
匹配的 `.deb`，用同一 Release 的 `SHA256SUMS` 校验，并在不自动启动通用服务的前提下安装
所需依赖，然后完成首次配置、管理员初始化与控制面启动。必须从有可写控制 TTY 的会话运行：
一次性管理员密码只显示在该终端，因此不支持无人值守安装。

```sh
curl -fLO https://github.com/three-b0dy/OpenSurge-for-Linux/releases/latest/download/opensurge-install
sudo bash ./opensurge-install
```

默认使用最新 Release；固定版本时传入 tag：

```sh
sudo bash ./opensurge-install --version vX.Y.Z
```

离线安装时，必须同时保留 `opensurge-install`、精确的
`opensurge_<version>_$(dpkg --print-architecture).deb` 与 `SHA256SUMS`。安装器不会为本地
包生成校验和，也没有跳过校验的选项。只有校验文件不在包旁边时，才使用
`--checksums /path/to/SHA256SUMS`：

```sh
sudo bash ./opensurge-install \
  --deb ./opensurge_<version>_$(dpkg --print-architecture).deb \
  --checksums /media/opensurge/SHA256SUMS
```

不要直接运行 `dpkg -i` 或 `apt install ./package.deb`，包括升级。包的 `preinst` 会故意
拒绝该路径，避免绕过安装器负责的依赖、DNS、53 端口与回滚事务。

全新安装会按照 IPv4 默认路由生成安全的 `same_lan` 控制平面配置：真实 Linux 接口名和
源地址同时用作 gateway/upstream 接口，DHCP 与透明代理保持关闭。`eth0`、`ens18`、
`enp1s0.50`、bridge 和 VLAN 均直接使用；`lan0` 与 `wan0` 仅是示例，不是别名。已有
`/etc/opensurge/config.yaml` 与管理员状态不会被覆盖。

要首次部署隔离 LAN，先创建 VLAN（如需要）并配置下游地址，再提供真实拓扑：

```sh
sudo bash ./opensurge-install --mode isolated_lan \
  --downstream-interface enp2s0.50 \
  --upstream-interface enp1s0 \
  --lan-ip 192.168.50.1 \
  --lan-cidr 192.168.50.0/24
```

两个接口必须不同，LAN IPv4 必须已经配置在下游接口上。安装器不会创建 VLAN、添加地址或
猜测上游。非 `/24` 的隔离 CIDR 还必须显式提供 CIDR 内、升序的
`--dhcp-range-start` 与 `--dhcp-range-end`；它不会推断新的 `same_wifi_dhcp` 拓扑。

网关数据面由 root 拥有；控制服务以受限的 `opensurge` 账号运行，只在配置的 LAN
`management.listen` IPv4 地址上提供 HTTPS Control API 与 GUI。首次安装会创建有效期十年
的 RSA-3072 自签名证书和单一 `admin` 账号，并仅在控制 TTY 显示一次性密码。登录 Web GUI
后立即修改它；密码不会进入安装器日志、命令参数或环境变量。确认显示的证书指纹后再接受
浏览器告警；也可以替换为 SAN 包含监听 IP 的已校验证书和私钥：

```sh
sudo opensurge-setup replace-certificate \
  --cert /etc/opensurge/tls/custom-cert.pem \
  --key /etc/opensurge/tls/custom-key.pem
```

忘记管理员密码时，在有控制 TTY 的主机上执行
`sudo opensurge-setup reset-password`。

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
清空全局防火墙规则，停止或回滚时会恢复已记录的 forwarding 状态。

安装器只把自己可能修改的主机状态保存至 `/var/lib/opensurge/install-state/`，非敏感诊断
写入 `/var/log/opensurge-install.log`。若 `systemd-resolved.service` 正在运行，它会选择安装前
第一个有效的非本地 IPv4/IPv6 `nameserver`，或在没有此 nameserver 时选择 IPv4 默认路由
网关；随后才可能停止/禁用 resolved 并以常规文件替换 `/etc/resolv.conf`。两个来源都不可用
会在依赖安装和 resolver 修改前停止。

安装器会记录通用 `dnsmasq.service` 的原状态，并在需要时停用/停止它，防止系统级服务与
OpenSurge 竞争。已知服务停止后，全新安装会拒绝任何剩余 TCP 或 UDP 53 端口监听者，报告
协议、地址、PID 与进程名，但不会停止、杀死或重配未知进程。安装器失败、包 remove 或 purge
时，只恢复清单证明为 OpenSurge 改动的 resolver 与通用服务状态；无效清单保留以供人工恢复。

`make test`、`make web-test`、`make installer-test` 与 `make linux-ci-check` 是确定性的
仓库门禁。包安装、配置校验或 HTTPS 状态端点响应仅是 package/config/startup smoke，不能
证明下游 DHCP、DNS、转发、NAT、回滚或透明流量。使用 `sudo -v && make linux-lab-test` 获得
namespace DHCP/DNS/NAT/回滚证据；使用 `sudo -v && make linux-lab-test-tun` 验证无显式代理
HTTPS 流量进入 mihomo TUN 日志。TUN 门禁用 `dig` 单独断言 DNS、再用 `curl --resolve` 固定
测试地址，不依赖 curl 是否支持 `--dns-servers`。Orb arm64 以及指定 QA 主机验收是独立证据，
没有各自命令输出时不能声称已运行。
