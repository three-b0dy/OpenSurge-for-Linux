// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ControlConfig, Overview } from '../types'

vi.mock('../api', () => ({
  api: {
    config: vi.fn(),
    networkInterfaces: vi.fn(),
    gatewayPlan: vi.fn(),
    saveConfig: vi.fn(),
    devicePolicy: vi.fn(),
    devices: vi.fn(),
  },
  waitForOperation: vi.fn(),
}))

import { api } from '../api'
import { NetworkPage } from './NetworkPage'

const overview = {
  status: { gateway: 'stopped' },
  recovery: { stage: 'idle', required: false },
} as unknown as Overview

const baseConfig: ControlConfig = {
  schema_version: 1,
  revision: 'config-r1',
  gateway: { mode: 'same_lan', interface: 'en0', lan_ip: '192.168.1.20', upstream_interface: 'en0' },
  dhcp: { enabled: false, range_start: '192.168.1.120', range_end: '192.168.1.199', lease_time: '12h', domain: 'lan' },
  dns: { listen: '192.168.1.20', upstream: '127.0.0.1#1053' },
  transparent: { mode: 'off', strict_route: false },
  device_policy: { enabled: false, protected_ipv4: [] },
}

function renderPage(config: ControlConfig) {
  const onChanged = vi.fn(async () => {})
  const onNavigate = vi.fn()
  vi.mocked(api.config).mockResolvedValue(config)
  render(<NetworkPage overview={overview} onChanged={onChanged} onNavigate={onNavigate} />)
  return { onChanged, onNavigate }
}

describe('NetworkPage', () => {
  beforeEach(() => {
    vi.mocked(api.networkInterfaces).mockResolvedValue({ schema_version: 1, interfaces: [] })
    vi.mocked(api.gatewayPlan).mockResolvedValue({ blockers: [], warnings: [], snapshot: {}, protected_ipv4: [], dhcp_servers: [] } as never)
    vi.mocked(api.saveConfig).mockImplementation(async config => config as never)
  })

  afterEach(() => { cleanup(); vi.clearAllMocks() })

  it('repairs an existing same-LAN off config to TUN before it can be started', async () => {
    renderPage(baseConfig)

    const transparent = await screen.findByRole('combobox', { name: '透明代理模式' })
    expect((transparent as HTMLSelectElement).value).toBe('tun')
    expect((transparent as HTMLSelectElement).disabled).toBe(true)
    expect(screen.getByText('有未保存的修改')).toBeTruthy()

    await userEvent.click(screen.getByRole('button', { name: '保存网络配置' }))
    await waitFor(() => expect(api.saveConfig).toHaveBeenCalledWith(expect.objectContaining({ transparent: expect.objectContaining({ mode: 'tun' }) })))
  })

  it('repairs an existing same-Wi-Fi-DHCP off config to TUN as well', async () => {
    renderPage({ ...baseConfig, gateway: { ...baseConfig.gateway, mode: 'same_wifi_dhcp' }, dhcp: { ...baseConfig.dhcp, enabled: true } })

    const transparent = await screen.findByRole('combobox', { name: '透明代理模式' })
    expect((transparent as HTMLSelectElement).value).toBe('tun')
    expect((transparent as HTMLSelectElement).disabled).toBe(true)
  })

  it('keeps isolated-LAN transparent mode configurable', async () => {
    renderPage({ ...baseConfig, gateway: { ...baseConfig.gateway, mode: 'isolated_lan', upstream_interface: 'en1' }, dhcp: { ...baseConfig.dhcp, enabled: true } })

    const transparent = await screen.findByRole('combobox', { name: '透明代理模式' })
    expect((transparent as HTMLSelectElement).value).toBe('off')
    expect((transparent as HTMLSelectElement).disabled).toBe(false)
    expect(screen.queryByText('有未保存的修改')).toBeNull()
  })
})
