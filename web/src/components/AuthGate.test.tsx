// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AuthGate } from './AuthGate'
import { api, RequestError } from '../api'

vi.mock('../api', () => ({
  RequestError: class RequestError extends Error {
    constructor(public status: number, public code: string, message: string) { super(message) }
  },
  api: {
    authStatus: vi.fn(),
    authSetup: vi.fn(),
    authLogin: vi.fn(),
  },
}))

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('AuthGate', () => {
  it('rejects a setup submission whose password confirmation does not match', async () => {
    vi.mocked(api.authStatus).mockResolvedValueOnce({ initialized: false, authenticated: false })
    const onAuthenticated = vi.fn()

    render(<AuthGate onAuthenticated={onAuthenticated} />)

    await screen.findByRole('heading', { name: '初始化管理员账户' })
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('密码'), 'correct-horse-battery-staple')
    await user.type(screen.getByLabelText('确认密码'), 'different-password-value')
    await user.click(screen.getByRole('button', { name: '创建管理员账户并登录' }))

    expect(await screen.findByText('两次输入的密码不一致')).toBeTruthy()
    expect(api.authSetup).not.toHaveBeenCalled()
    expect(onAuthenticated).not.toHaveBeenCalled()
  })

  it('shows an inline error and stays on the form when login credentials are rejected', async () => {
    vi.mocked(api.authStatus).mockResolvedValueOnce({ initialized: true, authenticated: false })
    vi.mocked(api.authLogin).mockRejectedValueOnce(new RequestError(401, 'authentication_required', 'username or password is incorrect'))
    const onAuthenticated = vi.fn()

    render(<AuthGate onAuthenticated={onAuthenticated} />)

    await screen.findByRole('heading', { name: '登录控制面板' })
    const user = userEvent.setup()
    const passwordInput = screen.getByLabelText('密码')
    const loginButton = screen.getByRole('button', { name: '登录' })
    await waitFor(() => expect(passwordInput.hasAttribute('disabled')).toBe(false))
    await user.type(passwordInput, 'wrong-password')
    await user.click(loginButton)

    expect(await screen.findByText('username or password is incorrect')).toBeTruthy()
    expect(onAuthenticated).not.toHaveBeenCalled()
  })

  it('switches to the login form when setup has already been completed by someone else', async () => {
    vi.mocked(api.authStatus).mockResolvedValueOnce({ initialized: false, authenticated: false })
    vi.mocked(api.authSetup).mockRejectedValueOnce(new RequestError(409, 'admin_initialized', 'administrator setup has already been completed'))
    const onAuthenticated = vi.fn()

    render(<AuthGate onAuthenticated={onAuthenticated} />)

    await screen.findByRole('heading', { name: '初始化管理员账户' })
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('密码'), 'correct-horse-battery-staple')
    await user.type(screen.getByLabelText('确认密码'), 'correct-horse-battery-staple')
    await user.click(screen.getByRole('button', { name: '创建管理员账户并登录' }))

    expect(await screen.findByRole('heading', { name: '登录控制面板' })).toBeTruthy()
    expect(api.authLogin).not.toHaveBeenCalled()
  })
})
