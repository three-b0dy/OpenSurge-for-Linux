import { useEffect, useState, type FormEvent } from 'react'
import { api, RequestError } from '../api'
import type { AuthStatus } from '../types'

export function AuthGate({ onAuthenticated }: { onAuthenticated: () => void }) {
  const [status, setStatus] = useState<AuthStatus | null>(null)
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false
    api.authStatus()
      .then(result => { if (!cancelled) setStatus(result) })
      .catch(() => { if (!cancelled) setStatus({ initialized: true, authenticated: false } as AuthStatus) })
    return () => { cancelled = true }
  }, [])

  const setupMode = status !== null && !status.initialized

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (busy) return
    setError('')
    if (setupMode && password !== confirmPassword) {
      setError('两次输入的密码不一致')
      return
    }
    setBusy(true)
    try {
      if (setupMode) {
        await api.authSetup(username, password)
      }
      await api.authLogin(username, password)
      onAuthenticated()
    } catch (cause) {
      if (cause instanceof RequestError) {
        if (cause.code === 'admin_initialized') {
          setStatus({ initialized: true, authenticated: false })
          setError('管理员账户已完成初始化，请改为使用该账户登录。')
        } else if (cause.code === 'setup_required') {
          setStatus({ initialized: false, authenticated: false })
          setError('管理员账户尚未初始化，请先设置账户。')
        } else {
          setError(cause.message)
        }
      } else {
        setError(cause instanceof Error ? cause.message : String(cause))
      }
    } finally {
      setBusy(false)
    }
  }

  return <section className="auth-gate" role="alert">
    <form className="registration-form auth-card" onSubmit={event => void submit(event)}>
      <div className="utility-card-heading"><span><small>OPENSURGE</small><h3>{setupMode ? '初始化管理员账户' : '登录控制面板'}</h3></span></div>
      <p className="card-help">{status === null ? '正在检查登录状态…' : setupMode ? '这是本机第一次使用，请设置管理员用户名和密码。' : '会话已过期或尚未登录，请使用管理员账户重新登录。'}</p>
      <label>用户名<input aria-label="用户名" autoFocus autoComplete="username" value={username} onChange={event => setUsername(event.target.value)} disabled={busy || status === null} /></label>
      <label>密码<input aria-label="密码" type="password" autoComplete={setupMode ? 'new-password' : 'current-password'} value={password} onChange={event => setPassword(event.target.value)} disabled={busy || status === null} /></label>
      {setupMode && <label>确认密码<input aria-label="确认密码" type="password" autoComplete="new-password" value={confirmPassword} onChange={event => setConfirmPassword(event.target.value)} disabled={busy} /></label>}
      {setupMode && <small className="registration-id-hint">管理员用户名在此确定，之后只能重置密码。</small>}
      {error && <small className="field-error" role="status">{error}</small>}
      <button className="primary" type="submit" disabled={busy || status === null || !username.trim() || !password || (setupMode && !confirmPassword)}>
        {busy ? '正在处理…' : setupMode ? '创建管理员账户并登录' : '登录'}
      </button>
    </form>
  </section>
}
