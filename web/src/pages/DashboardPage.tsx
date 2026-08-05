import { useState } from 'react'
import { api, waitForOperation } from '../api'
import { ActivityCard } from '../components/ActivityCard'
import { PageHeader } from '../components/Common'
import { DeviceTrafficPanel } from '../components/DeviceTrafficPanel'
import { GatewayActionDialog } from '../components/GatewayActionDialog'
import { GatewayHealthCard } from '../components/GatewayHealthCard'
import { LiveRateCard } from '../components/LiveRateCard'
import { TrafficTrendCard } from '../components/TrafficTrendCard'
import { gatewayActionResult, modeRunsDirectly, type GatewayAction } from '../gatewayControl'
import { useDeviceTraffic } from '../hooks/useDeviceTraffic'
import type { Overview } from '../types'

export function DashboardPage({ overview, onChanged, onOpenNetwork }: {
  overview: Overview | null
  onChanged: () => Promise<void>
  onOpenNetwork: (action: GatewayAction) => void
}) {
  const running = overview?.status.gateway === 'running' || overview?.status.gateway === 'degraded'
  const stopped = overview?.status.gateway === 'stopped'
  const { traffic, history, error } = useDeviceTraffic(overview?.status.gateway)
  const rates = traffic?.gateway_rates ?? { upload: 0, download: 0 }
  const [pending, setPending] = useState<GatewayAction | null>(null)
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState('')
  const [message, setMessage] = useState('')
  const mode = overview?.topology

  const request = (action: GatewayAction) => {
    // The DHCP takeover mode has to run through the recovery state machine, so it
    // is the only case that still hands off to the network page.
    if (!modeRunsDirectly(mode)) { onOpenNetwork(action); return }
    setActionError(''); setMessage(''); setPending(action)
  }

  const run = async () => {
    if (!pending) return
    setBusy(true); setActionError('')
    try {
      await waitForOperation((await api.gateway(pending)).id)
      await onChanged()
      setMessage(gatewayActionResult(mode, pending))
      setPending(null)
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : String(cause))
    } finally { setBusy(false) }
  }

  const action: GatewayAction = running ? 'stop' : 'start'
  return <>
    <PageHeader eyebrow="CONTROL CENTER" title="全屋网关，一眼可见" description="OpenSurge 负责网关生命周期；mihomo 是当前代理引擎。" action={<button className={running ? 'danger' : 'primary'} disabled={!overview || busy || (!running && !stopped)} onClick={() => request(action)}>{busy ? (action === 'start' ? '正在启动…' : '正在停止…') : running ? '停止网关' : '启动网关'}</button>} />
    {message && <div className="notice" role="status">{message}</div>}
    {actionError && !pending && <div className="notice warn" role="alert">{actionError}</div>}
    {overview?.warnings?.length ? <div className="dashboard-warning-stack" role="status">{overview.warnings.map(item => <div className="notice warn" key={item}>{item}</div>)}</div> : null}
    <section className="dashboard-live-grid">
      <GatewayHealthCard overview={overview} />
      <LiveRateCard direction="upload" value={rates.upload} history={history} />
      <LiveRateCard direction="download" value={rates.download} history={history} />
    </section>
    <section className="dashboard-monitor-grid">
      <ActivityCard traffic={traffic} />
      <TrafficTrendCard title="流量趋势" subtitle="网关全部 mihomo 活跃连接 · 近 60 秒内存采样" history={history} className="gateway-trend-card" />
    </section>
    <DeviceTrafficPanel gateway={overview?.status.gateway} traffic={traffic} history={history} error={error} />
    {pending && <GatewayActionDialog mode={mode} action={pending} busy={busy} error={actionError} onCancel={() => { if (!busy) { setPending(null); setActionError('') } }} onConfirm={() => void run()} />}
  </>
}
