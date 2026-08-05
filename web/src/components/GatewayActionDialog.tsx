import { useEffect } from 'react'
import {
  gatewayActionConsequences,
  gatewayActionSummary,
  gatewayActionTitle,
  type GatewayAction,
} from '../gatewayControl'

// Replaces window.confirm for gateway start/stop so the prompt matches the rest
// of the UI and can show the mode-specific consequences as a list.
export function GatewayActionDialog({ mode, action, busy, error, onCancel, onConfirm }: {
  mode: string | undefined
  action: GatewayAction
  busy: boolean
  error?: string
  onCancel: () => void
  onConfirm: () => void
}) {
  useEffect(() => {
    const closeOnEscape = (event: KeyboardEvent) => { if (event.key === 'Escape' && !busy) onCancel() }
    window.addEventListener('keydown', closeOnEscape)
    return () => window.removeEventListener('keydown', closeOnEscape)
  }, [busy, onCancel])

  const title = gatewayActionTitle(mode, action)
  return <dialog className="reload-dialog" open aria-modal="true" aria-labelledby="gateway-action-title">
    <h2 id="gateway-action-title">{title}</h2>
    <p>{gatewayActionSummary(mode, action)}</p>
    <ul>{gatewayActionConsequences(mode, action).map(item => <li key={item}>{item}</li>)}</ul>
    {error && <div className="notice warn" role="alert">{error}</div>}
    <div className="dialog-actions">
      <button type="button" disabled={busy} onClick={onCancel}>取消</button>
      <button className={action === 'stop' ? 'danger' : 'primary'} type="button" autoFocus disabled={busy} onClick={onConfirm}>
        {busy ? (action === 'start' ? '正在启动…' : '正在停止…') : (action === 'start' ? '确认启动' : '确认停止')}
      </button>
    </div>
  </dialog>
}
