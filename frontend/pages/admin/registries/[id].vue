<script setup lang="ts">
import {
  ArrowLeft,
  ExternalLink,
  RefreshCw,
  Settings2,
  Trash2,
  Flame,
  Eraser,
  HeartPulse,
  Search,
  Copy,
  Check,
  Lock,
  Network,
  Boxes,
} from 'lucide-vue-next'
import type {
  RegistryInstance,
  RegistryTypeInfo,
  ArtifactPage,
  ArtifactDescriptor,
} from '~/types/registry'
import { formatBytes, formatPercent, formatDateTime, copyText } from '~/utils/format'

definePageMeta({ layout: 'admin' })

const route = useRoute()
const router = useRouter()
const id = computed(() => String(route.params.id || ''))

const loading = ref(true)
const errorMsg = ref('')
const noticeMsg = ref('')
const reg = ref<RegistryInstance | null>(null)
const types = ref<RegistryTypeInfo[]>([])
const editOpen = ref(false)
const activeTab = ref<'overview' | 'versions' | 'artifacts' | 'cache' | 'danger'>('overview')

// Artifacts tab state
const artLoading = ref(false)
const artSearch = ref('')
const artOrigin = ref('')
const artPage = ref(1)
const artifacts = ref<ArtifactPage>({ items: [], total: 0, page: 1, limit: 20 })

// Cache actions state
const warmName = ref('')
const actionBusy = ref(false)
const copiedBase = ref(false)
const confirmDeleteSlug = ref('')

const fullBaseUrl = computed(() => {
  if (!reg.value) return ''
  if (typeof window === 'undefined') return reg.value.base_path
  return `${window.location.origin}${reg.value.base_path}`
})

const currentTypeInfo = computed(() =>
  reg.value ? types.value.find((t) => t.type === reg.value?.type) ?? null : null,
)

function formatConfigValue(val: any): string {
  if (val === undefined || val === null || val === '') return 'Default'
  if (typeof val === 'boolean') return val ? 'Enabled' : 'Disabled'
  if (Array.isArray(val)) return val.join(', ')
  return String(val)
}

async function loadRegistry() {
  loading.value = true
  errorMsg.value = ''
  try {
    const [r, t] = await Promise.all([
      apiFetch<RegistryInstance>(`/api/admin/registries/${id.value}`),
      apiFetch<{ types: RegistryTypeInfo[] }>('/api/admin/registry-types'),
    ])
    reg.value = r
    types.value = t.types || []
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to load registry'
  } finally {
    loading.value = false
  }
}

async function loadArtifacts() {
  if (!reg.value) return
  artLoading.value = true
  try {
    const params = new URLSearchParams({
      page: String(artPage.value),
      limit: '20',
    })
    if (artSearch.value.trim()) params.set('search', artSearch.value.trim())
    if (artOrigin.value) params.set('origin', artOrigin.value)
    artifacts.value = await apiFetch<ArtifactPage>(
      `/api/admin/registries/${reg.value.id}/artifacts?${params.toString()}`,
    )
  } catch {
    artifacts.value = { items: [], total: 0, page: 1, limit: 20 }
  } finally {
    artLoading.value = false
  }
}

async function handleDeleteArtifact(art: ArtifactDescriptor) {
  if (!reg.value) return
  if (!confirm(`Delete artifact "${art.name}" from ${reg.value.name}? Shared blobs referenced by other registries are preserved safely.`)) {
    return
  }
  errorMsg.value = ''
  try {
    await apiFetch(
      `/api/admin/registries/${reg.value.id}/artifacts/${encodeURIComponent(art.name)}`,
      { method: 'DELETE' },
    )
    noticeMsg.value = `Deleted "${art.name}"`
    await Promise.all([loadArtifacts(), loadRegistry()])
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to delete artifact'
  }
}

async function handlePurgeCache() {
  if (!reg.value) return
  if (!confirm(`Purge all cached upstream artifacts in ${reg.value.name}? Locally hosted artifacts are protected and will NOT be deleted.`)) {
    return
  }
  actionBusy.value = true
  errorMsg.value = ''
  noticeMsg.value = ''
  try {
    const res = await apiFetch<{ removed_refs: number; freed_logical_bytes: number }>(
      `/api/admin/registries/${reg.value.id}/cache/purge`,
      { method: 'POST' },
    )
    noticeMsg.value = `Cache purged: removed ${res.removed_refs} cached entries (${formatBytes(res.freed_logical_bytes)} logical freed).`
    await Promise.all([loadRegistry(), loadArtifacts()])
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to purge cache'
  } finally {
    actionBusy.value = false
  }
}

async function handleWarmCache() {
  if (!reg.value || !warmName.value.trim()) return
  actionBusy.value = true
  errorMsg.value = ''
  noticeMsg.value = ''
  try {
    await apiFetch(`/api/admin/registries/${reg.value.id}/cache/warm`, {
      method: 'POST',
      body: JSON.stringify({ name: warmName.value.trim() }),
    })
    noticeMsg.value = `Successfully warmed "${warmName.value.trim()}" into local cache.`
    warmName.value = ''
    await Promise.all([loadRegistry(), loadArtifacts()])
  } catch (err: any) {
    errorMsg.value = err?.message || 'Cache warm failed'
  } finally {
    actionBusy.value = false
  }
}

async function handleHealthCheck() {
  if (!reg.value) return
  actionBusy.value = true
  errorMsg.value = ''
  noticeMsg.value = ''
  try {
    const res = await apiFetch<{ available: boolean; status_code?: number; latency_ms: number; error?: string }>(
      `/api/admin/registries/${reg.value.id}/health-check`,
      { method: 'POST' },
    )
    noticeMsg.value = res.available
      ? `Upstream reachable${res.status_code ? ` (HTTP ${res.status_code})` : ''} in ${res.latency_ms} ms`
      : `Upstream check failed: ${res.error || 'unreachable'}`
    await loadRegistry()
  } catch (err: any) {
    errorMsg.value = err?.message || 'Health check failed'
  } finally {
    actionBusy.value = false
  }
}

async function handleDeleteRegistry() {
  if (!reg.value || confirmDeleteSlug.value !== reg.value.slug) return
  actionBusy.value = true
  errorMsg.value = ''
  try {
    await apiFetch(`/api/admin/registries/${reg.value.id}`, { method: 'DELETE' })
    await router.push('/admin/registries')
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to delete registry'
  } finally {
    actionBusy.value = false
  }
}

async function copyEndpoint() {
  if (await copyText(fullBaseUrl.value)) {
    copiedBase.value = true
    setTimeout(() => (copiedBase.value = false), 2000)
  }
}

onMounted(async () => {
  await loadRegistry()
  await loadArtifacts()
})
</script>

<template>
  <div class="space-y-6">
    <div>
      <NuxtLink
        to="/admin/registries"
        class="inline-flex items-center gap-1.5 text-xs font-medium text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft class="h-3.5 w-3.5" />
        Back to Registries
      </NuxtLink>
    </div>

    <SharedErrorBanner :message="errorMsg" />
    <div
      v-if="noticeMsg"
      class="rounded-lg border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-emerald-800"
    >
      {{ noticeMsg }}
    </div>

    <div v-if="loading && !reg" class="h-44 rounded-xl border bg-card animate-pulse" />

    <template v-else-if="reg">
      <!-- Header -->
      <div class="flex flex-col md:flex-row md:items-start justify-between gap-4">
        <div class="space-y-1.5">
          <div class="flex flex-wrap items-center gap-2">
            <h1 class="text-2xl font-bold tracking-tight">{{ reg.name }}</h1>
            <UiBadge variant="info" class="uppercase font-mono text-[10px]">{{ reg.type }}</UiBadge>
            <UiBadge variant="secondary" class="capitalize">{{ reg.mode }}</UiBadge>
            <UiBadge v-if="reg.is_default" variant="success">Default ({{ reg.type }})</UiBadge>
            <UiBadge :variant="reg.health === 'healthy' ? 'success' : reg.health === 'degraded' ? 'warning' : 'secondary'" class="capitalize">
              {{ reg.health }}
            </UiBadge>
          </div>
          <p class="text-sm text-muted-foreground">{{ reg.description || 'No description configured.' }}</p>
          <button
            type="button"
            class="inline-flex items-center gap-2 rounded bg-muted px-2.5 py-1 text-xs font-mono text-foreground hover:bg-muted/80"
            @click="copyEndpoint"
          >
            <span>{{ fullBaseUrl }}</span>
            <Check v-if="copiedBase" class="h-3 w-3 text-emerald-600" />
            <Copy v-else class="h-3 w-3 text-muted-foreground" />
          </button>
        </div>

        <div class="flex flex-wrap items-center gap-2">
          <NuxtLink :to="`/explore/${reg.slug}`">
            <UiButton variant="outline" size="sm">
              <ExternalLink class="h-3.5 w-3.5" />
              Open Explorer
            </UiButton>
          </NuxtLink>
          <UiButton variant="outline" size="sm" @click="editOpen = true">
            <Settings2 class="h-3.5 w-3.5" />
            Configure
          </UiButton>
          <UiButton variant="outline" size="sm" @click="loadRegistry">
            <RefreshCw class="h-3.5 w-3.5" />
          </UiButton>
        </div>
      </div>

      <!-- Capability-driven tabs -->
      <div class="flex items-center gap-1 border-b overflow-x-auto">
        <button
          v-for="tab in [
            { key: 'overview', label: 'Overview & Settings', show: true },
            { key: 'versions', label: 'Versions & Storage', show: reg.type === 'flutter' },
            { key: 'artifacts', label: `Artifacts (${artifacts.total})`, show: true },
            { key: 'cache', label: 'Upstream & Cache', show: reg.effective_capabilities.proxy },
            { key: 'danger', label: 'Danger Zone', show: true },
          ].filter((t) => t.show)"
          :key="tab.key"
          type="button"
          :class="[
            'border-b-2 px-4 py-2.5 text-sm font-medium whitespace-nowrap transition-colors',
            activeTab === tab.key
              ? 'border-primary text-primary'
              : 'border-transparent text-muted-foreground hover:text-foreground',
          ]"
          @click="activeTab = tab.key as any"
        >
          {{ tab.label }}
        </button>
      </div>

      <!-- OVERVIEW TAB -->
      <div v-if="activeTab === 'overview'" class="space-y-6">
        <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
          <UiCard class="p-4">
            <div class="text-xs text-muted-foreground">Downloads</div>
            <div class="mt-1 text-2xl font-bold">{{ reg.metrics.downloads }}</div>
            <div class="text-[11px] text-muted-foreground">{{ formatBytes(reg.metrics.bytes_served) }} served</div>
          </UiCard>
          <UiCard class="p-4">
            <div class="text-xs text-muted-foreground">Uploads</div>
            <div class="mt-1 text-2xl font-bold">{{ reg.metrics.uploads }}</div>
            <div class="text-[11px] text-muted-foreground">{{ formatBytes(reg.metrics.bytes_received) }} received</div>
          </UiCard>
          <UiCard class="p-4">
            <div class="text-xs text-muted-foreground">Cache Hit Ratio</div>
            <div class="mt-1 text-2xl font-bold">
              {{ reg.mode === 'hosted' ? 'N/A' : formatPercent(reg.metrics.cache_hit_ratio) }}
            </div>
            <div class="text-[11px] text-muted-foreground">
              {{ reg.metrics.cache_hits }} hits / {{ reg.metrics.cache_misses }} misses
            </div>
          </UiCard>
          <UiCard class="p-4">
            <div class="text-xs text-muted-foreground">Active Transfers</div>
            <div class="mt-1 text-2xl font-bold">{{ reg.metrics.active_transfers }}</div>
            <div class="text-[11px] text-muted-foreground">
              {{ reg.storage.total_refs }} artifact references
            </div>
          </UiCard>
        </div>

        <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
          <UiCard class="p-5 space-y-4">
            <h2 class="text-sm font-semibold">Storage &amp; Quota Utilization</h2>
            <SharedStorageBar
              :used-bytes="reg.storage.logical_bytes"
              :quota-bytes="reg.storage_quota_bytes"
              :hosted-bytes="reg.storage.hosted_bytes"
              :cached-bytes="reg.storage.cached_bytes"
            />
            <div class="grid grid-cols-2 gap-3 pt-2 text-xs">
              <div class="rounded-lg bg-muted/40 p-3">
                <div class="text-muted-foreground">Hosted Blobs (Protected)</div>
                <div class="font-semibold text-foreground mt-0.5">
                  {{ reg.storage.hosted_refs }} refs · {{ formatBytes(reg.storage.hosted_bytes) }}
                </div>
              </div>
              <div class="rounded-lg bg-muted/40 p-3">
                <div class="text-muted-foreground">Cached Blobs (Evictable)</div>
                <div class="font-semibold text-foreground mt-0.5">
                  {{ reg.storage.cached_refs }} refs · {{ formatBytes(reg.storage.cached_bytes) }}
                </div>
              </div>
            </div>
          </UiCard>

          <UiCard class="p-5 space-y-4">
            <h2 class="text-sm font-semibold">Effective Capabilities</h2>
            <p class="text-xs text-muted-foreground">
              Only actions permitted by the installed registry type and selected repository mode (<strong class="capitalize">{{ reg.mode }}</strong>) are enabled.
            </p>
            <SharedCapabilityBadges :capabilities="reg.effective_capabilities" />
            <div class="border-t pt-3 space-y-1 text-xs text-muted-foreground">
              <div>Visibility: <strong class="text-foreground capitalize">{{ reg.visibility }}</strong> (Anonymous read: {{ reg.auth_policy.allow_anonymous_read ? 'Yes' : 'No' }})</div>
              <div v-if="reg.hostname">Custom Hostname: <strong class="font-mono text-foreground">{{ reg.hostname }}</strong></div>
              <div v-if="reg.upstream_url">Upstream: <strong class="font-mono text-foreground">{{ reg.upstream_url }}</strong></div>
              <div>Updated: <strong class="text-foreground">{{ formatDateTime(reg.updated_at) }}</strong></div>
            </div>
          </UiCard>
        </div>

        <!-- Ecosystem-specific active settings summary -->
        <UiCard v-if="currentTypeInfo?.config_hints?.length" class="p-5 space-y-4">
          <div class="flex items-center justify-between gap-2">
            <div>
              <h2 class="text-sm font-semibold">{{ currentTypeInfo.display_name }} Configuration</h2>
              <p class="text-xs text-muted-foreground">
                Live ecosystem-specific settings applied to this instance.
              </p>
            </div>
            <UiButton variant="outline" size="sm" @click="editOpen = true">
              <Settings2 class="h-3.5 w-3.5" />
              Edit Settings
            </UiButton>
          </div>

          <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3 text-xs">
            <div
              v-for="hint in currentTypeInfo.config_hints"
              :key="hint.key"
              class="rounded-lg border bg-muted/20 p-3 space-y-1"
            >
              <div class="text-muted-foreground">{{ hint.label }}</div>
              <div class="font-mono font-medium text-foreground break-all">
                {{ formatConfigValue(reg.config?.[hint.key] ?? hint.default_value) }}
              </div>
            </div>
          </div>
        </UiCard>
      </div>

      <!-- VERSIONS & STORAGE TAB (Flutter Storage: FLUTTER_STORAGE_BASE_URL per-version registry) -->
      <div v-if="activeTab === 'versions' && reg.type === 'flutter'" class="space-y-4">
        <UiCard class="p-5 space-y-4">
          <div class="flex flex-wrap items-start justify-between gap-2">
            <div>
              <h2 class="text-sm font-semibold flex items-center gap-1.5">
                <Boxes class="h-4 w-4 text-primary" />
                FLUTTER_STORAGE_BASE_URL Settings
              </h2>
              <p class="text-xs text-muted-foreground">
                Storage categories, upstream repositories, release manifest source, and cache policy for
                this Flutter Storage registry. Changes apply live.
              </p>
            </div>
            <UiButton variant="outline" size="sm" @click="editOpen = true">
              <Settings2 class="h-3.5 w-3.5" />
              Edit Storage Settings
            </UiButton>
          </div>

          <div class="grid grid-cols-2 sm:grid-cols-4 gap-2.5 text-xs">
            <div class="rounded-md bg-muted/30 p-2.5">
              <div class="text-muted-foreground">Storage Name</div>
              <div class="font-medium text-foreground mt-0.5">
                {{ reg.config?.storage_name || reg.name }}
              </div>
            </div>
            <div class="rounded-md bg-muted/30 p-2.5">
              <div class="text-muted-foreground">Manifest Source</div>
              <div class="font-mono text-foreground mt-0.5 truncate" :title="reg.config?.releases_base_url">
                {{ reg.config?.releases_base_url || `${reg.upstream_url}/flutter_infra_release/releases` }}
              </div>
            </div>
            <div class="rounded-md bg-muted/30 p-2.5">
              <div class="text-muted-foreground">Manifest TTL</div>
              <div class="font-medium text-foreground mt-0.5">
                {{ reg.config?.manifest_ttl_seconds ?? 300 }}s
              </div>
            </div>
            <div class="rounded-md bg-muted/30 p-2.5">
              <div class="text-muted-foreground">Version Registry TTL</div>
              <div class="font-medium text-foreground mt-0.5">
                {{ reg.config?.registry_ttl_seconds ?? 900 }}s
              </div>
            </div>
          </div>
        </UiCard>

        <SharedFlutterVersionRegistry
          :endpoint="`/api/admin/registries/${reg.id}/version-registry`"
          unavailable-hint="The version registry is derived from upstream release manifests. Verify the registry upstream and release manifest base URL are reachable, then refresh."
        />
      </div>

      <!-- ARTIFACTS TAB -->
      <div v-if="activeTab === 'artifacts'" class="space-y-4">
        <div class="flex flex-wrap items-center gap-3">
          <form class="relative flex-1 min-w-[220px] max-w-md" @submit.prevent="() => { artPage = 1; loadArtifacts() }">
            <Search class="absolute left-3 top-2.5 h-4 w-4 text-muted-foreground" />
            <UiInput v-model="artSearch" placeholder="Filter artifacts by name or path..." class="pl-9" />
          </form>
          <UiSelect
            v-model="artOrigin"
            class="w-40"
            @update:model-value="() => { artPage = 1; loadArtifacts() }"
          >
            <option value="">All Origins</option>
            <option value="hosted">Hosted</option>
            <option value="cached">Cached</option>
          </UiSelect>
          <UiButton variant="outline" size="sm" @click="loadArtifacts">Refresh</UiButton>
        </div>

        <SharedArtifactTable
          :items="artifacts.items"
          :registry-slug="reg.slug"
          :registry-type="reg.type"
          :can-delete="reg.effective_capabilities.artifact_deletion"
          :loading="artLoading"
          @delete="handleDeleteArtifact"
        />

        <SharedPaginationBar
          :page="artPage"
          :limit="20"
          :total="artifacts.total"
          @change="(p) => { artPage = p; loadArtifacts() }"
        />
      </div>

      <!-- UPSTREAM & CACHE TAB (Capability-gated!) -->
      <div v-if="activeTab === 'cache' && reg.effective_capabilities.proxy" class="grid grid-cols-1 lg:grid-cols-2 gap-4">
        <UiCard class="p-5 space-y-4">
          <div class="flex items-center justify-between gap-2">
            <h2 class="text-sm font-semibold flex items-center gap-1.5">
              <Network class="h-4 w-4 text-primary" />
              Upstream Configuration &amp; Health
            </h2>
            <div class="flex items-center gap-1.5">
              <UiButton variant="outline" size="sm" @click="editOpen = true">
                <Settings2 class="h-3.5 w-3.5" />
                Edit
              </UiButton>
              <UiButton variant="outline" size="sm" :disabled="actionBusy" @click="handleHealthCheck">
                <HeartPulse class="h-3.5 w-3.5" />
                Check Now
              </UiButton>
            </div>
          </div>

          <div class="space-y-2 text-xs">
            <div>Upstream URL: <span class="font-mono text-foreground">{{ reg.upstream_url }}</span></div>
            <div class="flex items-center gap-2">
              <span>Status:</span>
              <UiBadge
                :variant="!reg.metrics.upstream.checked ? 'secondary' : reg.metrics.upstream.available ? 'success' : 'destructive'"
              >
                {{ !reg.metrics.upstream.checked ? 'Not checked yet' : reg.metrics.upstream.available ? 'Available' : 'Unavailable' }}
              </UiBadge>
            </div>
            <div v-if="reg.metrics.upstream.checked">
              Latency: <strong>{{ reg.metrics.upstream.latency_ms }} ms</strong>
              <span v-if="reg.metrics.upstream.status_code"> · HTTP {{ reg.metrics.upstream.status_code }}</span>
              · Checked {{ formatDateTime(reg.metrics.upstream.checked_at) }}
            </div>
            <div v-if="reg.metrics.upstream.error" class="text-red-600">
              {{ reg.metrics.upstream.error }}
            </div>
          </div>

          <div class="border-t pt-3 grid grid-cols-2 gap-2.5 text-xs">
            <div class="rounded-md bg-muted/30 p-2.5">
              <div class="text-muted-foreground">Authentication</div>
              <div class="font-medium text-foreground capitalize mt-0.5 flex items-center gap-1">
                {{ reg.upstream_config?.auth_type || 'Anonymous' }}
                <Lock v-if="reg.upstream_config?.has_auth_secret" class="h-3 w-3 text-emerald-600" />
              </div>
            </div>
            <div class="rounded-md bg-muted/30 p-2.5">
              <div class="text-muted-foreground">Proxy Override</div>
              <div class="font-mono text-foreground mt-0.5 truncate">
                {{ reg.upstream_config?.proxy_url || 'Global default' }}
              </div>
            </div>
            <div class="rounded-md bg-muted/30 p-2.5">
              <div class="text-muted-foreground">Timeout Override</div>
              <div class="font-medium text-foreground mt-0.5">
                {{ reg.upstream_config?.timeout_seconds ? `${reg.upstream_config.timeout_seconds}s` : 'Default' }}
              </div>
            </div>
            <div class="rounded-md bg-muted/30 p-2.5">
              <div class="text-muted-foreground">TLS Verification</div>
              <div class="font-medium text-foreground mt-0.5">
                {{ reg.upstream_config?.insecure_skip_verify ? 'Skip Verify (Insecure)' : 'Strict (Global)' }}
              </div>
            </div>
          </div>
        </UiCard>

        <div class="space-y-4">
          <UiCard class="p-5 space-y-4">
            <div class="flex items-center justify-between">
              <h2 class="text-sm font-semibold">Cache Policy &amp; Eviction</h2>
              <UiButton variant="outline" size="sm" @click="editOpen = true">
                <Settings2 class="h-3.5 w-3.5" />
                Edit Policy
              </UiButton>
            </div>
            <div class="grid grid-cols-2 gap-2.5 text-xs">
              <div class="rounded-md bg-muted/30 p-2.5">
                <div class="text-muted-foreground">Metadata TTL</div>
                <div class="font-medium text-foreground mt-0.5">{{ reg.cache_policy.metadata_ttl_seconds }}s</div>
              </div>
              <div class="rounded-md bg-muted/30 p-2.5">
                <div class="text-muted-foreground">Archive TTL</div>
                <div class="font-medium text-foreground mt-0.5">
                  {{ reg.cache_policy.archive_ttl_seconds ? `${reg.cache_policy.archive_ttl_seconds}s` : 'Keep until evicted' }}
                </div>
              </div>
              <div class="rounded-md bg-muted/30 p-2.5">
                <div class="text-muted-foreground">Max Cache Size</div>
                <div class="font-medium text-foreground mt-0.5">
                  {{ reg.cache_policy.max_size_bytes ? formatBytes(reg.cache_policy.max_size_bytes) : 'Unlimited' }}
                </div>
              </div>
              <div class="rounded-md bg-muted/30 p-2.5">
                <div class="text-muted-foreground">Eviction Policy</div>
                <div class="font-medium text-foreground uppercase mt-0.5">{{ reg.cache_policy.eviction_policy || 'LRU' }}</div>
              </div>
            </div>
            <div class="border-t pt-3 space-y-2">
              <p class="text-xs text-muted-foreground">
                Evicts all cached upstream artifacts in this registry. Locally hosted artifacts are strictly protected and remain untouched.
              </p>
              <UiButton variant="outline" size="sm" :disabled="actionBusy" @click="handlePurgeCache">
                <Eraser class="h-3.5 w-3.5" />
                Purge Cached Artifacts
              </UiButton>
            </div>
          </UiCard>

          <UiCard v-if="reg.effective_capabilities.cache_warm" class="p-5 space-y-4">
            <h2 class="text-sm font-semibold">Warm Package Cache</h2>
            <p class="text-xs text-muted-foreground">
              Pre-fetch a package's metadata and latest archive from upstream into local content-addressed storage.
            </p>
            <form class="flex items-center gap-2" @submit.prevent="handleWarmCache">
              <UiInput v-model="warmName" placeholder="Package name (e.g. flutter_bloc)" required />
              <UiButton type="submit" size="sm" :disabled="actionBusy">
                <Flame class="h-3.5 w-3.5" />
                Warm
              </UiButton>
            </form>
          </UiCard>
        </div>
      </div>

      <!-- DANGER ZONE TAB -->
      <UiCard v-if="activeTab === 'danger'" class="border-red-200 p-6 space-y-4">
        <div>
          <h2 class="text-base font-semibold text-red-700">Delete Registry Instance</h2>
          <p class="text-xs text-muted-foreground mt-1">
            Permanently removes <strong class="font-mono text-foreground">{{ reg.slug }}</strong>, its metadata, and its artifact references. Any physical blobs deduplicated with other registries are preserved safely.
          </p>
        </div>
        <div class="max-w-sm space-y-2">
          <label class="text-xs text-muted-foreground">
            Type <strong class="font-mono text-foreground">{{ reg.slug }}</strong> to confirm deletion:
          </label>
          <UiInput v-model="confirmDeleteSlug" :placeholder="reg.slug" />
          <UiButton
            variant="destructive"
            size="sm"
            :disabled="confirmDeleteSlug !== reg.slug || actionBusy"
            @click="handleDeleteRegistry"
          >
            <Trash2 class="h-3.5 w-3.5" />
            Delete Registry Permanently
          </UiButton>
        </div>
      </UiCard>

      <SharedRegistryFormModal
        :open="editOpen"
        :types="types"
        :editing="reg"
        @close="editOpen = false"
        @saved="loadRegistry"
      />
    </template>
  </div>
</template>
