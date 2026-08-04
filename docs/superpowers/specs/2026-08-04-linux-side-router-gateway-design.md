# OpenSurge for Linux 旁路由网关设计

**日期：** 2026-08-04
**状态：** 已确认，待实施计划
**产品：** OpenSurge for Linux

## 目标与范围

将仓库主分支改造为 Debian/Ubuntu 上的 Linux-only OpenSurge 网关。它提供 CLI、systemd
数据面、LAN HTTPS Control API 与现有 React Web GUI，并支持 amd64 和 arm64 的 GitHub
Release `.deb` 安装包。

首个 Linux 版本完整覆盖现有网关的三种部署语义：

1. `same_lan`：同 LAN 的选择性旁路由。主路由继续提供 DHCP；已登记设备的 IPv4 网关和 DNS
   手动设置，或由主路由 reservation 设置为 Linux 网关。OpenSurge 不运行 DHCP。
2. `same_wifi_dhcp`：同 LAN DHCP 接管。操作者先在主路由关闭 DHCP，再明确确认这一事实；Linux
   上的 dnsmasq 才能启动并向该 LAN 下发 DHCP/DNS。
3. `isolated_lan`：独立下游 LAN。Linux 的第二有线网卡或 VLAN 提供下游网络，OpenSurge
   提供 DHCP、DNS、NAT 和透明代理。

不支持 Wi-Fi AP/SSID 承载下游 LAN，也不再支持 macOS。SwiftUI 菜单栏应用、launchd、PF、
`networksetup`、macOS 系统代理协同、macOS 安装器和 macOS 专属验证均从主分支移除或替换为
Linux 版本。产品名称保持 OpenSurge for Linux；mihomo 只是代理引擎名称。

IPv4 是 v1 的网关协议。`same_lan` 与 `same_wifi_dhcp` 不承诺拦截由主路由直接提供的 IPv6
路径，UI 与 doctor 必须明确显示该限制。`isolated_lan` 不下发 IPv6 配置，并丢弃下游 IPv6
转发，避免把 IPv6 直通误描述为受代理保护。

## 总体架构

保留并演进可复用控制面：Go CLI、配置模型、mihomo profile overlay、设备策略、Control API、
嵌入式 React Web GUI、设备/连接/策略/诊断 DTO。将 macOS 平台调用替换为窄的 Linux 平台层：

| 职责 | Linux 实现 |
| --- | --- |
| IP 地址、接口、路由与邻居发现 | `iproute2`；Go 适配器解析并执行固定命令 |
| IPv4 forwarding | `sysctl net.ipv4.ip_forward`，记录启动前值后恢复 |
| NAT、转发、管理/DHCP/DNS 放行 | OpenSurge 专属 `nftables` table/chains |
| 透明代理 | mihomo TUN 的 `auto-route` 与 `auto-redirect` |
| DHCP/DNS | system `dnsmasq` 二进制 |
| 网关生命周期 | root 运行的 systemd gateway 服务 |
| LAN Web 管理 | 受限 `opensurge` 用户运行的 Control API systemd 服务 |

透明模式固定为 `transparent.mode: tun`。OpenSurge 渲染 mihomo TUN 时启用
`auto-route: true`、`auto-redirect: true` 和 DNS hijack；不引入 `redir-port`、iptables
REDIRECT 或自建 TPROXY 作为并行透明路径。mihomo 负责自身 TUN 的 policy routing 与重定向
规则；OpenSurge 只负责其命名空间内的防火墙规则。

OpenSurge 的 nftables 规则只存在于命名为 `opensurge` 的 table/chains 中，内容包括 masquerade、
必要的转发许可、下游 DHCP/DNS 和管理端口许可。停止或 rollback 只删除这套 table。不得 flush
全局 ruleset、删除其它 table，或改写 UFW/firewalld 的规则。v1 的唯一受支持后端是 nftables；
若 UFW/firewalld 或其他策略阻塞流量，doctor 给出具体诊断和修复提示，而不静默绕过。

## 服务、权限和控制面

安装生成三个 systemd 单元：

- `opensurge-gateway.service`：root 长驻服务，拥有 gateway runtime state、dnsmasq、mihomo、
  nftables 与 forwarding 的生命周期。
- `opensurge-gateway.socket`：root 拥有的 Unix socket。只有 `opensurge` 组可连接；协议只允许
  start、stop、reload、restart-mihomo、status 和受校验的配置动作。
- `opensurge-control.service`：以专用、无登录 shell 的 `opensurge` 用户运行，提供嵌入式 React
  Web GUI 和 Go Control API，并通过 gateway socket 发起固定的特权动作。

CLI `opensurge` 保留机器可读的 `status`、`doctor`、`logs`、`snapshot`、`leases`、`devices`、
`policies`、`connections` 和 provider 操作。它经 socket 查询服务；改变网关状态的本地 CLI
操作需要 sudo，避免将 root 权限交给浏览器进程。

Control API 必须配置一个精确的 `management.listen` IPv4 地址和端口，且该地址属于已声明的 LAN
管理接口；拒绝 `0.0.0.0`、回环地址和外网接口绑定。默认端口是 `61767`。服务仅提供 HTTPS，
没有 HTTP 降级路径。

认证采用普通家用路由器模型：单一管理员用户名与密码；首次安装在终端强制创建账户；密码用
Argon2id 哈希保存。登录成功后创建 12 小时空闲失效的服务端会话，以 `HttpOnly`、`Secure`、
`SameSite=Strict` cookie 传递；所有 mutation 验证 Origin。每个源 IP 在 15 分钟内连续失败
5 次后锁定 15 分钟。不存在多用户、角色、OIDC、云账户或 API 长期 bearer token。

安装时生成 10 年有效期的 RSA-3072 自签名证书，私钥仅由 root 和 `opensurge` 组读取。管理员可通过
CLI 或 Web GUI 原子替换成自有证书和私钥；替换前验证证书/私钥匹配、文件权限和预期监听名。
忘记密码时只能由本地 sudo 用户运行 `opensurge admin reset-password` 重置。

## 配置与模式契约

用户可继续使用 gateway、DHCP、DNS、mihomo profile、来源和 device policy 的业务语义。
渲染层接管 mihomo 的 LAN binding、DNS、TUN、external controller 和 route-related 字段；导入
profile 不能覆盖这些字段。每设备策略继续用 `SRC-IP-CIDR` 和 dnsmasq reservation 实现。

新增 Linux 配置迁移检查：它读取已有 macOS 配置，删除 `pf`、macOS network service 和
`local_system_proxy` 字段，要求操作者显式映射上游/下游接口、VLAN、管理监听地址和网络模式。
迁移只生成候选配置；只有 `opensurge config validate` 成功后才可替换 live 配置。

`same_lan` 预检要求 gateway 与 upstream 使用同一接口、DHCP 关闭、Linux LAN IPv4 已存在，且
已登记设备没有与 Linux/受保护地址冲突的静态 IP。它不会尝试修改主路由 DHCP。

`same_wifi_dhcp` 预检除上述网段/租约保护外，还要求请求中带有明确的 `router_dhcp_disabled`
确认。开始前 UI 显示恢复检查表；停止时只撤销 OpenSurge 服务、nftables 和 forwarding，永不
声称已重新启用主路由 DHCP。

`isolated_lan` 预检要求上游接口与下游有线接口/VLAN 不同、下游地址和 DHCP pool 不重叠，并且
dnsmasq 只绑定下游接口。它向下游下发 Linux 的 IPv4 为 router 和 DNS。

## 生命周期、失败处理与可观测性

所有模式遵循同一顺序：

1. 静态预检二进制、root 能力、接口、地址/子网、端口、TUN、nftables 和 policy-routing 冲突。
2. 渲染并以 `mihomo -t`、`dnsmasq --test` 和 `nft --check` 验证候选 artifacts。
3. 原子写入 runtime state，保存启动前 IPv4 forwarding 值以及 OpenSurge 管理的 table 标识。
4. 启用 forwarding，启动 mihomo，并在 10 秒内等待运行时 TUN ready；显式 `tun.enable: false`
   或 TUN startup error 一律失败。
5. 按模式启动或跳过 dnsmasq，最后原子加载 OpenSurge nftables table。
6. 只在全部 ready 后报告 running。

任何失败都按相反顺序停止 dnsmasq 和 mihomo、删除 OpenSurge table、恢复 forwarding。清理失败时
保留 runtime state，并以 degraded/cleanup-required 状态报告，允许管理员重试 stop；绝不把
残留网络状态报告为 stopped。`reload` 先在独立临时目录预渲染/验证，再执行完整 stop/start；
不承诺零中断。`restart-mihomo` 保留 DHCP、nftables 和 forwarding，仅替换 mihomo 并重新执行
TUN readiness。

`status`、`doctor`、`logs` 和 `snapshot` 维持文本与 JSON 输出。systemd journal 是服务日志的
权威来源；API 会返回相应的最近日志、运行态和每项局部诊断错误。

## 打包、安装、升级和卸载

GitHub Release 为每个版本构建：

- `opensurge_<version>_amd64.deb`
- `opensurge_<version>_arm64.deb`

每个包包含已校验 checksum 的对应架构 mihomo、OpenSurge Go 二进制、嵌入式 Web assets、systemd
unit、默认配置和安装/迁移工具。`Depends` 明确列出 `dnsmasq`、`nftables`、`iproute2`、
`ca-certificates` 和 systemd。安装脚本创建系统用户/组、状态和配置目录、首次登录材料及默认
证书，但不在未完成网络配置时自动启动 gateway。

升级保留 `/etc/opensurge` 的配置、管理员凭据、证书、订阅凭据和 device policies；新二进制先
通过 migration/validation 后才替换 active runtime。卸载先停止 gateway，删除 OpenSurge runtime
state/table 并禁用 unit。包管理器默认保留配置文件；purge 才删除配置、凭据和证书。

## 验证与发布门槛

| 门槛 | 证明范围 |
| --- | --- |
| `make test` | Go/React 单元测试、配置迁移、nftables rendering、认证、systemd/CLI 配置生成 |
| `make linux-lab-test` | Linux network namespaces、veth、dnsmasq、nftables 下的 DHCP、DNS、NAT、rollback、接口/子网保护和 stop 清理 |
| `make linux-lab-test-tun` | 无显式代理客户端的 HTTPS 进入 mihomo TUN、DNS hijack、TUN readiness 及失败 rollback |
| `make linux-real-device-smoke` | 单网卡 same-LAN、双网卡/VLAN isolated LAN 的真实硬件证据；不替代 lab gate |
| release package test | 在受支持的 Debian 与 Ubuntu VM 安装 `.deb`、首次认证、systemd 启停、升级与 purge 语义 |

涉及 DHCP、DNS、mihomo、nftables、forwarding、rollback 或网关生命周期的改动，只有实际运行
`make linux-lab-test` 后才能声称数据面经过验证。涉及透明代理的改动，只有实际运行
`make linux-lab-test-tun` 后才能声称 TUN 路径经过验证。

## 上游镜像分支

已创建并推送 `upstream` 分支，初始值与 `YTwsy/OpenSurge-for-Mac` 的 `master` 相同。它是只读的
镜像审查输入，不是 Linux 开发分支。

新增 GitHub Actions 工作流，采用每天 `03:17 UTC` 的 cron（`17 3 * * *`）和
`workflow_dispatch`：

1. fetch `https://github.com/YTwsy/OpenSurge-for-Mac.git master`。
2. 若 commit SHA 未变，正常退出。
3. 若 SHA 变化，使用 guarded force-push 将该 commit 精确镜像到
   `three-b0dy/OpenSurge-for-Linux:upstream`。
4. 工作流使用最小的 `contents: write` 权限并记录镜像的旧/新 SHA。

`master` 永不由这个工作流写入，也不自动 merge `upstream`。上游 macOS 改动只能由维护者挑选
可复用的控制面提交，在 Linux 分支人工审查、适配和验证后引入。仓库应为 `upstream` 启用分支
保护，禁止人工功能提交，仅允许该同步 workflow 更新。

## 实施分解

这是一份完整的产品架构设计，但不能作为一次性改动实施。后续必须按以下顺序编写并执行独立的
实施计划，避免仓库长期停在 macOS/Linux 混合数据面：

1. **Linux 平台基础与仓库转向：** 建立 Linux platform interfaces、nftables renderer、systemd
   unit/权限模型、Linux 配置迁移与文档骨架；移除 macOS 打包和 runner 依赖。
2. **三种网关模式与实验室：** 实现 iproute2、forwarding、dnsmasq、mihomo TUN、nftables 生命周期，
   并建立 namespace lab、TUN lab 与真实设备 smoke。
3. **LAN 控制面：** 实现 HTTPS、单管理员登录、证书/密码恢复，改造 Control API、Web GUI 和 CLI
   以适配 Linux 服务边界。
4. **发行与维护：** 构建双架构 `.deb`、安装/升级/purge 验收、GitHub Release、每日 upstream
   镜像 workflow 和 Linux CI。

每个阶段完成时都运行与其数据面影响相符的验证门槛；阶段 2 是第一个可以宣称实际 Linux gateway
流量路径已通过验证的阶段。

## 验收标准

1. Debian 12+ 和 Ubuntu 22.04+ 可安装对应 amd64/arm64 `.deb`，并成功创建/启动 systemd 服务。
2. 管理 UI 只以 HTTPS 监听配置的 LAN IPv4；首次安装必须设置管理员账户，错误密码受速率限制，
   本地 CLI 可恢复密码。
3. 三种模式均有可验证的 preflight、start、stop、rollback 与恢复说明；`same_wifi_dhcp` 从不
   自动声称恢复了外部路由器 DHCP。
4. 无显式代理客户端的 IPv4 HTTPS 通过 mihomo TUN，有对应日志/测试证据；显式 mixed-port
   仍可用，设备策略与 imported profile 能保留功能。
5. OpenSurge stop 或失败 rollback 不残留 OpenSurge 的 nftables table、dnsmasq/mihomo 进程或
   被修改的 IPv4 forwarding；若清理失败则保留 state 并向 operator 报错。
6. macOS-only 代码、打包、文档与验证已从 Linux 主分支移除或替换，`make test` 和 Linux CI
   不依赖 macOS runner。
7. `upstream` 每日镜像工作流和手动触发均能在不改写 Linux `master` 的前提下更新镜像分支。
