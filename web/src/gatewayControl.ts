import type { ControlConfig } from './types'

export type GatewayMode = ControlConfig['gateway']['mode']
export type GatewayAction = 'start' | 'stop'

// same_wifi_dhcp takes over the router's DHCP, so it is only reachable through
// the recovery state machine on the network page. The other modes start and stop
// directly from whichever page the user is on.
export function modeRunsDirectly(mode: string | undefined): mode is Exclude<GatewayMode, 'same_wifi_dhcp'> {
  return mode === 'same_lan' || mode === 'isolated_lan'
}

export function gatewayModeLabel(mode: string | undefined) {
  if (mode === 'same_wifi_dhcp') return '同网 DHCP 接管'
  return mode === 'isolated_lan' ? '独立下游 LAN' : '旁路由模式'
}

export function gatewayActionTitle(mode: string | undefined, action: GatewayAction) {
  return `${action === 'start' ? '启动' : '停止'}${gatewayModeLabel(mode)}？`
}

export function gatewayActionSummary(mode: string | undefined, action: GatewayAction) {
  if (mode === 'same_lan') {
    return action === 'start'
      ? '将按已保存的网络配置启动旁路由模式：DNS、mihomo TUN、nftables/NAT 与 IPv4 forwarding。'
      : '将停止旁路由模式，释放 DNS、mihomo TUN、nftables/NAT 与 IPv4 forwarding。'
  }
  return action === 'start'
    ? '将按已保存的网络配置启动独立下游 LAN：DHCP/DNS、nftables/NAT 与 IPv4 forwarding。'
    : '将停止独立下游 LAN 的 DHCP/DNS、nftables/NAT 与 IPv4 forwarding。'
}

export function gatewayActionConsequences(mode: string | undefined, action: GatewayAction): string[] {
  if (mode === 'same_lan') {
    return action === 'start'
      ? ['路由器 DHCP 不会被关闭。', '部分设备需要自行把网关和 DNS 指向网关主机。', '已有连接会重新建立。']
      : ['仍把网关或 DNS 指向网关主机的设备可能立即断网。', '路由器 DHCP 不受影响。']
  }
  return action === 'start'
    ? ['下游客户端将从 OpenSurge 获取 DHCP 与 DNS。', '已有连接会重新建立。']
    : ['独立下游 LAN 客户端将失去 OpenSurge 提供的 DHCP/DNS 和网关连接。']
}

export function gatewayActionResult(mode: string | undefined, action: GatewayAction) {
  return `${gatewayModeLabel(mode)}${action === 'start' ? '已启动' : '已停止'}。`
}
