# Linux 控制面架构

OpenSurge 的控制面由 CLI、Control API 和 Web GUI 组成。它们共享配置校验、
mihomo 渲染、设备策略和 Linux 网络状态模型；控制面是已安装 Linux 网关服务的一部分。

## 页面与 API 边界

- 配置页负责读取、校验和保存用户明确提交的配置。
- Policies 页只展示 imported/managed profile 中的一般策略组和 provider。
- Devices 页管理设备身份、固定 IPv4、profile 和设备策略覆盖。
- Network、Connectivity 和 Recovery 页展示 Linux 网关状态，并把尚未实现的动作明确
  标记为需要管理员在物理网络或上游路由器上完成的操作。
- API 通过普通 policy-select、device-policy 和 provider 操作控制可见资源；不暴露
  主机专属的隐式策略组或兼容端点。

## 数据面方向

Linux 网关使用 iproute2 检查接口和邻居，使用 sysctl 管理 IPv4 forwarding，使用
OpenSurge 自己的 nftables table 管理转发/NAT 规则，并由 systemd 生命周期管理。root
网关服务经受限 Unix socket 接受固定的特权请求；`opensurge` 用户运行的控制服务仅在
`management.listen` 上以 HTTPS 提供 API/GUI。mihomo TUN 是唯一透明路径。
`isolated_lan` 的下游 IPv6 forwarding 显式丢弃；其余模式只报告未托管的 IPv6 路径。

## 安全与恢复

迁移只产生候选 YAML，不覆盖源文件；接口、上游路由和管理监听地址必须人工映射。
控制面使用单管理员会话认证。首次安装由 `opensurge-install` 生成十年自签名证书与
`admin` 账号，并仅在有可写控制 TTY 的会话显示一次性密码；不能用直接 `.deb` 安装或手动
初始化替代该受控流程。之后可用 `opensurge-setup replace-certificate` 替换为自有证书，或在
控制 TTY 上用 `opensurge-setup reset-password` 恢复管理员访问。恢复流程不得
改变上游路由器 DHCP；操作员必须在外部设备上记录任何 DHCP 变更。

Web 构建产物位于 `internal/webui/dist`，修改 `web/src` 后运行 `make web-build`，再
运行 `make web-test`。
