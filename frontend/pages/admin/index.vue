<script setup lang="ts">
import {
  HardDrive,
  Download,
  Upload,
  Activity,
  Gauge,
  Database,
  RefreshCw,
  ArrowUpRight,
  Layers,
} from 'lucide-vue-next'
import type { StatsOverview } from '~/types/registry'
import { formatBytes, formatPercent } from '~/utils/format'

definePageMeta({ layout: 'admin' })

const loading = ref(true)
const errorMsg = ref('')
const overview = ref<StatsOverview | null>(null)

async function loadOverview() {
  loading.value = true
  errorMsg.value = ''
  try {
    overview.value = await apiFetch<StatsOverview>('/api/admin/stats/overview')
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to load monitoring overview'
  } finally {
    loading.value = false
  }
}

const healthBadge = (h: string) => {
  if (h === 'healthy') return 'success'
  if (h === 'degraded') return 'warning'
  if (h === 'unhealthy') return 'destructive'
  return 'secondary'
}

const dailySeries = computed(() => {
  const buckets = overview.value?.history || []
  const byDate = new Map<string, { date: string; downloads: number; uploads: number }>()
  for (const b of buckets) {
    const cur = byDate.get(b.bucket_date) || { date: b.bucket_date, downloads: 0, uploads: 0 }
    cur.downloads += b.downloads
    cur.uploads += b.uploads
    byDate.set(b.bucket_date, cur)
  }
  return Array.from(byDate.values()).sort((a, b) => a.date.localeCompare(b.date))
})

const maxDailyTotal = computed(() => {
  let max = 1
  for (const d of dailySeries.value) {
    const t = d.downloads + d.uploads
    if (t > max) max = t
  }
  return max
})

onMounted(loadOverview)
</script>

<template>
  <div class="space-y-6">
    <div class="flex flex-wrap items-center justify-between gap-4">
      <div>
        <h1 class="text-2xl font-bold tracking-tight">Platform Overview</h1>
        <p class="text-sm text-muted-foreground">
          Global and per-registry storage, cache performance, traffic, and upstream health.
        </p>
      </div>
      <div class="flex items-center gap-2">
        <UiButton variant="outline" size="sm" :disabled="loading" @click="loadOverview">
          <RefreshCw :class="['h-3.5 w-3.5', loading ? 'animate-spin' : '']" />
          Refresh
        </UiButton>
        <NuxtLink to="/admin/registries">
          <UiButton size="sm">Manage Registries</UiButton>
        </NuxtLink>
      </div>
    </div>

    <SharedErrorBanner :message="errorMsg" />

    <div v-if="loading && !overview" class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
      <div v-for="n in 4" :key="n" class="h-28 rounded-xl border bg-card animate-pulse" />
    </div>

    <template v-else-if="overview">
      <!-- Top KPI grid -->
      <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        <UiCard class="p-5">
          <div class="flex items-center justify-between text-xs text-muted-foreground">
            <span>Physical Storage</span>
            <HardDrive class="h-4 w-4 text-primary" />
          </div>
          <div class="mt-2 text-2xl font-bold tracking-tight">
            {{ formatBytes(overview.storage.physical_bytes) }}
          </div>
          <div class="mt-1 text-xs text-muted-foreground">
            Logical: <strong class="text-foreground">{{ formatBytes(overview.storage.logical_bytes) }}</strong>
            <span v-if="overview.storage.deduplication_saved_bytes > 0" class="text-emerald-600 font-medium">
              (−{{ formatBytes(overview.storage.deduplication_saved_bytes) }} dedup)
            </span>
          </div>
        </UiCard>

        <UiCard class="p-5">
          <div class="flex items-center justify-between text-xs text-muted-foreground">
            <span>Total Downloads</span>
            <Download class="h-4 w-4 text-sky-600" />
          </div>
          <div class="mt-2 text-2xl font-bold tracking-tight">{{ overview.downloads }}</div>
          <div class="mt-1 text-xs text-muted-foreground">
            Bandwidth served: <strong class="text-foreground">{{ formatBytes(overview.bytes_served) }}</strong>
          </div>
        </UiCard>

        <UiCard class="p-5">
          <div class="flex items-center justify-between text-xs text-muted-foreground">
            <span>Total Uploads</span>
            <Upload class="h-4 w-4 text-indigo-600" />
          </div>
          <div class="mt-2 text-2xl font-bold tracking-tight">{{ overview.uploads }}</div>
          <div class="mt-1 text-xs text-muted-foreground">
            Received: <strong class="text-foreground">{{ formatBytes(overview.bytes_received) }}</strong>
          </div>
        </UiCard>

        <UiCard class="p-5">
          <div class="flex items-center justify-between text-xs text-muted-foreground">
            <span>Cache Hit Ratio</span>
            <Gauge class="h-4 w-4 text-emerald-600" />
          </div>
          <div class="mt-2 text-2xl font-bold tracking-tight">
            {{ formatPercent(overview.cache_hit_ratio) }}
          </div>
          <div class="mt-1 flex items-center justify-between text-xs text-muted-foreground">
            <span>{{ overview.cache_hits }} hits / {{ overview.cache_misses }} misses</span>
            <span class="inline-flex items-center gap-1 font-medium text-foreground">
              <Activity class="h-3 w-3 text-emerald-500" />
              {{ overview.active_transfers }} active
            </span>
          </div>
        </UiCard>
      </div>

      <!-- Storage deduplication + Installed Registry Types -->
      <div class="grid grid-cols-1 lg:grid-cols-3 gap-4">
        <UiCard class="p-5 lg:col-span-2 space-y-4">
          <div class="flex items-center justify-between">
            <div>
              <h2 class="text-sm font-semibold">Daily Transfer Activity</h2>
              <p class="text-xs text-muted-foreground">Aggregated bounded-cardinality counters across all registries</p>
            </div>
            <div class="flex items-center gap-3 text-[11px] text-muted-foreground">
              <span class="inline-flex items-center gap-1"><span class="h-2 w-2 rounded-full bg-primary" /> Downloads</span>
              <span class="inline-flex items-center gap-1"><span class="h-2 w-2 rounded-full bg-indigo-400" /> Uploads</span>
            </div>
          </div>

          <div v-if="dailySeries.length === 0" class="py-10 text-center text-xs text-muted-foreground">
            No transfer history recorded yet. Downloads and publishes will populate daily buckets automatically.
          </div>
          <div v-else class="flex items-end gap-2 h-36 pt-4">
            <div
              v-for="d in dailySeries"
              :key="d.date"
              class="flex-1 flex flex-col items-center gap-1.5 min-w-0"
            >
              <div class="w-full max-w-[32px] h-24 flex flex-col justify-end gap-0.5">
                <div
                  v-if="d.uploads > 0"
                  class="w-full rounded-t bg-indigo-400 transition-all"
                  :style="{ height: `${Math.max(4, (d.uploads / maxDailyTotal) * 100)}%` }"
                  :title="`${d.date}: ${d.uploads} uploads`"
                />
                <div
                  v-if="d.downloads > 0"
                  class="w-full rounded-t bg-primary transition-all"
                  :style="{ height: `${Math.max(4, (d.downloads / maxDailyTotal) * 100)}%` }"
                  :title="`${d.date}: ${d.downloads} downloads`"
                />
              </div>
              <span class="text-[10px] font-mono text-muted-foreground truncate">{{ d.date.slice(5) }}</span>
            </div>
          </div>
        </UiCard>

        <UiCard class="p-5 space-y-4">
          <div class="flex items-center justify-between">
            <h2 class="text-sm font-semibold">Installed Registry Types</h2>
            <Layers class="h-4 w-4 text-muted-foreground" />
          </div>
          <div class="space-y-3">
            <div
              v-for="t in overview.installed_types"
              :key="t.type"
              class="rounded-lg border bg-muted/30 p-3 space-y-1.5"
            >
              <div class="flex items-center justify-between">
                <span class="text-sm font-semibold text-foreground">{{ t.display_name }}</span>
                <UiBadge variant="info" class="font-mono uppercase text-[10px]">{{ t.type }}</UiBadge>
              </div>
              <p class="text-xs text-muted-foreground">{{ t.description }}</p>
              <div class="text-[11px] text-muted-foreground pt-1">
                Modes: <strong class="text-foreground capitalize">{{ t.supported_modes.join(', ') }}</strong>
              </div>
            </div>
          </div>
          <p class="text-[11px] text-muted-foreground">
            Content-addressed blobs stored: <strong class="text-foreground">{{ overview.storage.physical_blob_count }}</strong>
            ({{ overview.storage.logical_ref_count }} registry references)
          </p>
        </UiCard>
      </div>

      <!-- Per-registry Health & Utilization Table -->
      <UiCard class="overflow-hidden">
        <div class="flex items-center justify-between border-b px-5 py-4">
          <div class="flex items-center gap-2">
            <Database class="h-4 w-4 text-primary" />
            <h2 class="text-sm font-semibold">Per-Registry Health &amp; Statistics</h2>
          </div>
          <span class="text-xs text-muted-foreground">{{ overview.registry_count }} configured</span>
        </div>
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="border-b bg-muted/40 text-left text-xs font-medium text-muted-foreground">
                <th class="px-5 py-3">Registry</th>
                <th class="px-4 py-3">Type / Mode</th>
                <th class="px-4 py-3">Health</th>
                <th class="px-4 py-3">Logical Storage</th>
                <th class="px-4 py-3">Downloads / Uploads</th>
                <th class="px-4 py-3">Cache Hit</th>
                <th class="px-4 py-3 text-right">Inspect</th>
              </tr>
            </thead>
            <tbody class="divide-y">
              <tr v-for="reg in overview.registries" :key="reg.id" class="hover:bg-muted/20">
                <td class="px-5 py-3.5">
                  <div class="font-medium text-foreground">{{ reg.name }}</div>
                  <div class="font-mono text-xs text-muted-foreground">/r/{{ reg.slug }}</div>
                </td>
                <td class="px-4 py-3.5">
                  <div class="flex items-center gap-1.5">
                    <UiBadge variant="info" class="uppercase font-mono text-[10px]">{{ reg.type }}</UiBadge>
                    <UiBadge variant="secondary" class="capitalize">{{ reg.mode }}</UiBadge>
                  </div>
                </td>
                <td class="px-4 py-3.5">
                  <UiBadge :variant="healthBadge(reg.health)" class="capitalize">{{ reg.health }}</UiBadge>
                </td>
                <td class="px-4 py-3.5 min-w-[170px]">
                  <SharedStorageBar
                    :used-bytes="reg.storage.logical_bytes"
                    :quota-bytes="reg.storage_quota_bytes"
                    compact
                  />
                </td>
                <td class="px-4 py-3.5 text-xs text-muted-foreground">
                  <strong class="text-foreground">{{ reg.metrics.downloads }}</strong> dl /
                  <strong class="text-foreground">{{ reg.metrics.uploads }}</strong> ul
                </td>
                <td class="px-4 py-3.5 text-xs font-medium">
                  {{ reg.mode === 'hosted' ? '—' : formatPercent(reg.metrics.cache_hit_ratio) }}
                </td>
                <td class="px-4 py-3.5 text-right">
                  <NuxtLink
                    :to="`/admin/registries/${reg.id}`"
                    class="inline-flex items-center gap-1 text-xs font-medium text-primary hover:underline"
                  >
                    Manage
                    <ArrowUpRight class="h-3.5 w-3.5" />
                  </NuxtLink>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </UiCard>
    </template>
  </div>
</template>
