// 与后端 C5 的响应契约一一对应（父任务 design §7）。
// 若发现后端少返回字段，回到 C5 补充，不要在前端做推导或拼接。

export type KeyState = 'available' | 'cooling' | 'exhausted' | 'invalid'

export interface UpstreamKey {
  id: number
  name: string
  masked: string // fc-**** + 末 4 位，永不返回明文
  state: KeyState
  cooldown_remaining: number // 秒
  credits_total: number | null
  credits_remaining: number | null
  credits_synced_at: string | null
  request_count: number
  last_error: string | null
  enabled: boolean
  created_at: string
}

export interface ProxyKey {
  id: number
  name: string
  key_prefix: string
  request_count: number
  last_used_at: string | null
  created_at: string
  revoked: boolean
}

/** 创建代理 Key 的响应：plaintext_key 是明文唯一一次出现的位置。 */
export interface ProxyKeyCreated extends ProxyKey {
  plaintext_key: string
}

export interface Overview {
  credits_remaining_sum: number
  credits_total_sum: number
  key_counts: Record<string, number>
  proxy_key_count: number
  last_refreshed_at: string | null
}

export interface CreditRefresh {
  credits_total: number
  credits_remaining: number
  credits_synced_at: string
}

export interface SessionStatus {
  authenticated: boolean
}

export interface UpstreamKeyPatch {
  name?: string
  enabled?: boolean
  reset?: boolean
}
