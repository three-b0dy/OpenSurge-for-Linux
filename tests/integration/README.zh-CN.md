# 集成测试

策略控制面门禁会在真实 mihomo external controller 上验证可复用的 Linux 控制面契约，
但不宣称完整网关网络路径已经实现：

```sh
make policy-control-test
```

测试会渲染 imported profile，启动 mihomo 及 file/HTTP provider，验证一般策略组、策略
选择、连接、provider 刷新和 snapshot，再重启 mihomo 验证选中策略的持久化。它也覆盖源
地址限定的本地/私网规则和设备策略覆盖。

它不证明 DHCP、DNS、nftables 转发、TUN 流量、回滚或远程代理出口；这些结论需要后续
Linux lab gate。测试时不要在普通家庭或办公 LAN 上启用 DHCP 服务。
