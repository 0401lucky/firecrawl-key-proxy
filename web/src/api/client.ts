import { reactive } from 'vue'
import type {
  CallStats,
  CreditRefresh,
  Overview,
  ProxyKey,
  ProxyKeyCreated,
  SessionStatus,
  StatsWindow,
  UpstreamKey,
  UpstreamKeyPatch,
} from './types'

/** 全局登录态。ready 在启动时的会话检查完成后置真。 */
export const authState = reactive({
  authenticated: false,
  ready: false,
})

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

interface RequestOpts {
  /** 登录接口的 401 不触发全局跳转（已身在登录页，需展示「密码错误」）。 */
  silent401?: boolean
}

export async function request<T>(
  path: string,
  init?: RequestInit,
  opts?: RequestOpts,
): Promise<T> {
  const res = await fetch(path, {
    credentials: 'same-origin',
    ...init,
    headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) },
  })

  if (res.status === 401) {
    if (!opts?.silent401) {
      authState.authenticated = false
      // 整页跳转：重置前端状态，避免残留内存态。
      if (location.pathname !== '/login') location.assign('/login')
    }
    const body = await res.json().catch(() => null)
    throw new ApiError(401, (body as { message?: string })?.message ?? '未登录或会话已过期')
  }

  if (!res.ok) {
    let message = `请求失败 (${res.status})`
    try {
      const body = (await res.json()) as { message?: string }
      if (body.message) message = body.message
    } catch {
      /* 非 JSON 错误体，用默认文案 */
    }
    throw new ApiError(res.status, message)
  }

  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

export const api = {
  session: () => request<SessionStatus>('/api/admin/session'),
  login: (password: string) =>
    request<void>('/api/admin/login', {
      method: 'POST',
      body: JSON.stringify({ password }),
    }, { silent401: true }),
  logout: () => request<void>('/api/admin/logout', { method: 'POST' }),

  overview: () => request<Overview>('/api/admin/overview'),
  stats: (window: StatsWindow = '24h') =>
    request<CallStats>(`/api/admin/stats?window=${window}`),

  upstreamKeys: () => request<UpstreamKey[]>('/api/admin/upstream-keys'),
  createUpstreamKey: (name: string, apiKey: string) =>
    request<UpstreamKey>('/api/admin/upstream-keys', {
      method: 'POST',
      body: JSON.stringify({ name, api_key: apiKey }),
    }),
  patchUpstreamKey: (id: number, patch: UpstreamKeyPatch) =>
    request<UpstreamKey>(`/api/admin/upstream-keys/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(patch),
    }),
  deleteUpstreamKey: (id: number) =>
    request<void>(`/api/admin/upstream-keys/${id}`, { method: 'DELETE' }),
  refreshCredits: (id: number) =>
    request<CreditRefresh>(`/api/admin/upstream-keys/${id}/refresh-credits`, {
      method: 'POST',
    }),

  proxyKeys: () => request<ProxyKey[]>('/api/admin/proxy-keys'),
  createProxyKey: (name: string) =>
    request<ProxyKeyCreated>('/api/admin/proxy-keys', {
      method: 'POST',
      body: JSON.stringify({ name }),
    }),
  deleteProxyKey: (id: number) =>
    request<void>(`/api/admin/proxy-keys/${id}`, { method: 'DELETE' }),
}
