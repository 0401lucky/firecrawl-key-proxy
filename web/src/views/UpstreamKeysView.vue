<script setup lang="ts">
import { onUnmounted, ref } from 'vue'
import { api } from '../api/client'
import type { UpstreamKey } from '../api/types'
import { usePolling } from '../composables/usePolling'
import { useToast } from '../composables/useToast'
import StateBadge from '../components/StateBadge.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'

const keys = ref<UpstreamKey[]>([])
const { push } = useToast()

const formOpen = ref(false)
const newName = ref('')
const newKey = ref('')
const creating = ref(false)

const deleting = ref<UpstreamKey | null>(null)
const busyId = ref<number | null>(null)

async function load() {
  keys.value = await api.upstreamKeys()
}
usePolling(load, 5000)

async function create() {
  if (creating.value) return
  creating.value = true
  try {
    const created = await api.createUpstreamKey(newName.value.trim(), newKey.value.trim())
    keys.value.unshift(created)
    formOpen.value = false
    newName.value = ''
    newKey.value = ''
    push('success', `已录入「${created.name}」`)
  } catch (e) {
    push('error', (e as Error).message)
  } finally {
    creating.value = false
  }
}

async function patch(k: UpstreamKey, patch: { enabled?: boolean; reset?: boolean }) {
  if (busyId.value === k.id) return
  busyId.value = k.id
  try {
    const updated = await api.patchUpstreamKey(k.id, patch)
    const idx = keys.value.findIndex((x) => x.id === k.id)
    if (idx >= 0) keys.value[idx] = updated
    if (patch.reset) push('success', `已重置「${updated.name}」状态`)
  } catch (e) {
    push('error', (e as Error).message)
  } finally {
    busyId.value = null
  }
}

// 测试可用性：调 credit-usage（不消耗 credits），顺带把余额刷新回来。
// 成功即说明这个 Key 现在能正常访问 Firecrawl。
async function testKey(k: UpstreamKey) {
  if (busyId.value === k.id) return
  busyId.value = k.id
  try {
    const r = await api.refreshCredits(k.id)
    const idx = keys.value.findIndex((x) => x.id === k.id)
    if (idx >= 0) {
      keys.value[idx] = {
        ...keys.value[idx],
        credits_remaining: r.credits_remaining,
        credits_total: r.credits_total,
        credits_synced_at: r.credits_synced_at,
      }
    }
    push('success', `「${k.name}」可用，剩余 ${r.credits_remaining} credits`)
  } catch (e) {
    // 后端已按 401/402/429/网络错误给出可操作的提示，直接透出。
    push('error', `「${k.name}」${(e as Error).message}`)
  } finally {
    busyId.value = null
  }
}

async function confirmDelete() {
  if (!deleting.value) return
  try {
    await api.deleteUpstreamKey(deleting.value.id)
    keys.value = keys.value.filter((x) => x.id !== deleting.value!.id)
    push('success', `已删除「${deleting.value.name}」`)
  } catch (e) {
    push('error', (e as Error).message)
  } finally {
    deleting.value = null
  }
}

// 冷却倒计时（同总览页：后端快照 + 本地递减，轮询校正）
const now = ref(Date.now())
const cd = window.setInterval(() => (now.value = Date.now()), 1000)
onUnmounted(() => window.clearInterval(cd))

function cooldownText(k: UpstreamKey): string {
  if (k.state !== 'cooling') return '—'
  const secs = Math.max(0, k.cooldown_remaining - Math.floor((Date.now() - now.value) / 1000))
  if (secs <= 0) return '即将恢复'
  const m = Math.floor(secs / 60)
  return m > 0 ? `${m}分${secs % 60}秒` : `${secs}秒`
}
</script>

<template>
  <div class="max-w-6xl animate-rise">
    <header class="flex items-center justify-between">
      <h1 class="font-mono text-xl font-semibold tracking-wide t-primary">上游 Key</h1>
      <button class="btn-primary" @click="formOpen = true">+ 录入新 Key</button>
    </header>

    <!-- 新增表单 -->
    <div v-if="formOpen" class="surface mt-6 border-amber-400/60 p-5">
      <div class="grid gap-4 md:grid-cols-[1fr_2fr_auto]">
        <input v-model="newName" placeholder="备注名，如：免费账号 A" class="field" />
        <input
          v-model="newKey"
          placeholder="粘贴 Firecrawl API Key（fc-…）"
          class="field num"
          @keydown.enter="create"
        />
        <div class="flex gap-2">
          <button :disabled="creating" class="btn-primary" @click="create">
            {{ creating ? '提交中…' : '提交' }}
          </button>
          <button class="btn-ghost" @click="formOpen = false">取消</button>
        </div>
      </div>
    </div>

    <!-- 表格 -->
    <div class="surface mt-6 overflow-x-auto">
      <table class="w-full min-w-[900px] text-left text-sm">
        <thead>
          <tr class="border-b hairline bg-slate-50 dark:bg-ink/40">
            <th class="t-label px-4 py-3">名称 / Key</th>
            <th class="t-label px-4 py-3">状态</th>
            <th class="t-label px-4 py-3">冷却剩余</th>
            <th class="t-label px-4 py-3">剩余额度</th>
            <th class="t-label px-4 py-3">调用数</th>
            <th class="t-label px-4 py-3">最后错误</th>
            <th class="t-label px-4 py-3 text-right">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="k in keys"
            :key="k.id"
            class="border-b hairline last:border-0 hover:bg-slate-50 dark:hover:bg-ink-line/30"
            :class="k.enabled ? '' : 'opacity-60'"
          >
            <td class="px-4 py-3">
              <div class="font-medium t-primary">{{ k.name }}</div>
              <div class="num text-[11px] t-muted">{{ k.masked }}</div>
            </td>
            <td class="px-4 py-3"><StateBadge :state="k.state" :disabled="!k.enabled" /></td>
            <td class="num px-4 py-3 t-secondary">{{ cooldownText(k) }}</td>
            <td class="num px-4 py-3 t-body">
              {{ k.credits_remaining ?? '—' }}
              <span v-if="k.credits_total" class="text-[11px] t-muted">/ {{ k.credits_total }}</span>
            </td>
            <td class="num px-4 py-3 t-secondary">{{ k.request_count }}</td>
            <td
              class="max-w-[200px] truncate px-4 py-3 text-xs"
              :class="k.last_error ? 'text-red-600 dark:text-red-400' : 't-muted'"
              :title="k.last_error ?? ''"
            >
              {{ k.last_error ?? '—' }}
            </td>
            <td class="px-4 py-3">
              <div class="flex justify-end gap-1.5 text-xs">
                <button class="btn-mini" :disabled="busyId === k.id" @click="patch(k, { enabled: !k.enabled })">
                  {{ k.enabled ? '禁用' : '启用' }}
                </button>
                <button
                  v-if="k.state === 'exhausted' || k.state === 'invalid'"
                  class="rounded border border-amber-500/60 px-2 py-1 text-amber-700 transition-colors hover:bg-amber-400/10 disabled:opacity-50 dark:text-amber-300"
                  :disabled="busyId === k.id"
                  @click="patch(k, { reset: true })"
                >
                  重置
                </button>
                <button
                  class="btn-mini"
                  :disabled="busyId === k.id"
                  title="调用 Firecrawl 额度接口验证可用性，不消耗 credits"
                  @click="testKey(k)"
                >
                  {{ busyId === k.id ? '测试中…' : '测试' }}
                </button>
                <button
                  class="rounded border border-red-500/60 px-2 py-1 text-red-600 transition-colors hover:bg-red-500/10 dark:text-red-400"
                  @click="deleting = k"
                >
                  删除
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
      <p v-if="keys.length === 0" class="px-6 py-10 text-center text-sm t-secondary">
        还没有上游 Key——点击右上角「录入新 Key」开始。
      </p>
    </div>

    <ConfirmDialog
      :open="deleting !== null"
      title="删除上游 Key？"
      :message="`删除「${deleting?.name ?? ''}」后，该 Key 的进行中任务将无法再查询（job 映射会一并删除）。此操作不可撤销。`"
      confirm-text="确认删除"
      danger
      @confirm="confirmDelete"
      @cancel="deleting = null"
    />
  </div>
</template>
