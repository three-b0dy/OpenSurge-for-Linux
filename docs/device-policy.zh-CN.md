# 每设备策略覆盖

OpenSurge 只运行一个 mihomo 进程。设备策略为这个进程增加 selector group 和源 IPv4
规则，不会为每台设备复制进程或替换 imported profile。

## 配置

```yaml
device_policy:
  file: ./devices.json
```

JSON 文件可以包含 `devices`、`profiles`、`templates` 和 `rule_sets`，空文件也合法。设备
IPv4 必须唯一、位于网关 `/24` 内，并且不能是网段、广播或网关地址。`protected_ipv4` 可
用于保护路由器和恢复设备地址。

每台设备可以有 id、显示名称、MAC、IPv4、profile 和 `egress_mode`：

- `inherit_global` 先应用设备覆盖规则，再继续使用 imported/managed 网关规则和 terminal
  `MATCH`。
- `dedicated` 为未命中的公网流量生成 `device/<id>/default`，局域网和私网目标保持
  `DIRECT`。

规则可以选择已有策略组或 `DIRECT`、`REJECT`、`REJECT-DROP`、`REJECT-TINYGIF` 等内置
目标。带 `policies` 的规则生成 `device/<id>/<rule-id>`。`device/` 和
`open-surge-ruleset-` 是生成内容的保留命名空间；普通 imported 策略组、provider、
profile 选择和设备策略都会保留。

## 匹配与校验

域名、IP、协议、端口和 rule-set 按 JSON 模型组合。源地址规则先于设备覆盖，设备规则
先于网关规则。Imported `MATCH` 必须位于末尾。UDP 不支持时，selector 默认追加同条件
的 `REJECT`；只有明确配置 `fallthrough` 才会允许继续匹配。

```sh
opensurge devices --config ./config.yaml --format json
opensurge device-policy-select --config ./config.yaml \
  --device alice-phone --slot default --policy Proxy
```

selector 命令只改变指定设备 slot。运行中的 Linux gateway lifecycle 会将有效策略渲染给
单个 mihomo 进程；API 对校验或运行时错误返回明确结果，不伪造网络动作已成功。

## 身份边界

当前数据面按源 IPv4 匹配。DHCP 模式可以使用 MAC 绑定租约；共享 LAN 可以用固定 IPv4
并附带可选身份元数据。邻居表或连接观察只能作为人工复核证据，不能替代 DHCP 身份验证。
IPv6 设备身份尚未实现。
