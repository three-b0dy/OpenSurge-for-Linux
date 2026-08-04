# Linux 控制面架构

OpenSurge 的控制面由 CLI、Control API 和 Web GUI 组成。它们共享配置校验、
mihomo 渲染、设备策略和 Linux 网络状态模型；当前阶段不把控制面伪装成已经完成的
网关服务。

## 页面与 API 边界

- 配置页负责读取、校验和保存用户明确提交的配置。
- Policies 页只展示 imported/managed profile 中的一般策略组和 provider。
- Devices 页管理设备身份、固定 IPv4、profile 和设备策略覆盖。
- Network、Connectivity 和 Recovery 页展示 Linux 网关状态，并把尚未实现的动作明确
  标记为由 Linux gateway lifecycle 管理。
- API 通过普通 policy-select、device-policy 和 provider 操作控制可见资源；不暴露
  主机专属的隐式策略组或兼容端点。

## 数据面方向

Linux 网关使用 iproute2 检查接口和邻居，使用 sysctl 管理 IPv4 forwarding，使用
OpenSurge 自己的 nftables table 管理转发/NAT 规则，并以 systemd 作为后续生命周期方向。
mihomo TUN 是唯一透明路径。`isolated_lan` 的下游 IPv6 forwarding 显式丢弃；其余模式
只报告未托管的 IPv6 路径。

## 安全与恢复

迁移只产生候选 YAML，不覆盖源文件；接口、上游路由和管理监听地址必须人工映射。
控制面在网络动作未由 Linux lifecycle 实现时返回清晰的 unsupported 状态，不报告虚假的
成功。恢复流程不得改变上游路由器 DHCP；操作员必须在外部设备上记录任何 DHCP 变更。

Web 构建产物位于 `internal/webui/dist`，修改 `web/src` 后运行 `make web-build`，再
运行 `make web-test`。
