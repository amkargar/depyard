<script setup lang="ts">
import {
  RefreshCw,
  Search,
  Layers,
  Download,
  ExternalLink,
  GitBranch,
  AlertTriangle,
  Copy,
  Check,
} from 'lucide-vue-next'
import type { FlutterVersionRegistry, FlutterVersionEntry } from '~/types/registry'
import { formatDateTime, copyText } from '~/utils/format'

/**
 * Read-only Flutter SDK Version Registry viewer.
 *
 * Renders every Flutter version the mirror can serve, together with the effective
 * FLUTTER_STORAGE_BASE_URL storage settings used to resolve SDK / engine / Dart artifacts per
 * version. Used by the Admin Console and the public Flutter Storage Explorer.
 */

const props = defineProps<{
  /** Admin API endpoint returning { registry: FlutterVersionRegistry }. */
  endpoint: string
  /** When true, renders a compact "storage settings" summary above the version table. */
  showStorage?: boolean
  /** Message shown when upstream manifests are unreachable. */
  unavailableHint?: string
}>()

const loading = ref(true)
const errorMsg = ref('')
const data = ref<FlutterVersionRegistry | null>(null)
const search = ref('')
const channelFilter = ref('__all__')
const expanded = ref<string | null>(null)
const copiedKey = ref('')

async function load() {
  loading.value = true
  errorMsg.value = ''
  try {
    const res = await apiFetch<{ registry?: FlutterVersionRegistry } & FlutterVersionRegistry>(props.endpoint)
    // Admin endpoint wraps the snapshot as { registry: {...} }; the registry-native endpoint
    // (GET /r/<slug>/api/registry) returns the snapshot directly.
    data.value = (res.registry ?? (res as FlutterVersionRegistry)) || null
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to load Flutter version registry'
    data.value = null
  } finally {
    loading.value = false
  }
}

const channels = computed(() => {
  const set = new Set<string>()
  for (const v of data.value?.versions || []) {
    if (v.channel) set.add(v.channel)
  }
  return Array.from(set).sort()
})

const channelOptions = computed(() => [
  { value: '__all__', label: 'All Channels' },
  ...channels.value.map((ch) => ({ value: ch, label: ch })),
])

const filtered = computed<FlutterVersionEntry[]>(() => {
  const q = search.value.trim().toLowerCase()
  return (data.value?.versions || []).filter((v) => {
    if (channelFilter.value && channelFilter.value !== '__all__' && v.channel !== channelFilter.value) return false
    if (!q) return true
    return (
      v.version.toLowerCase().includes(q) ||
      (v.dart_sdk_version || '').toLowerCase().includes(q) ||
      (v.hash || '').toLowerCase().includes(q)
    )
  })
})

function toggleExpand(version: string) {
  expanded.value = expanded.value === version ? null : version
}

async function copySnippet(key: string, text: string) {
  if (await copyText(text)) {
    copiedKey.value = key
    setTimeout(() => {
      if (copiedKey.value === key) copiedKey.value = ''
    }, 2000)
  }
}

const currentChannelVersions = computed(() => data.value?.channels || [])

onMounted(load)
</script>

<template>
  <div class="space-y-4">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div>
        <h2 class="text-sm font-semibold flex items-center gap-1.5">
          <Layers class="h-4 w-4 text-primary" />
          Flutter SDK Version Registry
        </h2>
        <p class="text-xs text-muted-foreground">
          Every Flutter release this mirror can serve, with the FLUTTER_STORAGE_BASE_URL resolution used
          for each version. Generated from the upstream <code class="font-mono">releases_*.json</code> manifests.
        </p>
      </div>
      <UiButton variant="outline" size="sm" :disabled="loading" @click="load">
        <RefreshCw class="h-3.5 w-3.5" />
        Refresh Registry
      </UiButton>
    </div>

    <SharedErrorBanner :message="errorMsg" />
    <div
      v-if="errorMsg && unavailableHint"
      class="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-900"
    >
      {{ unavailableHint }}
    </div>

    <div v-if="loading && !data" class="h-32 rounded-xl border bg-card animate-pulse" />

    <template v-else-if="data">
      <!-- Summary: channels -->
      <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
        <div
          v-for="ch in currentChannelVersions"
          :key="ch.name"
          class="rounded-lg border bg-card p-3 space-y-1"
        >
          <div class="flex items-center gap-1.5 text-xs font-medium capitalize text-muted-foreground">
            <GitBranch class="h-3.5 w-3.5" />
            {{ ch.name }} channel
          </div>
          <div class="font-mono text-sm font-semibold">{{ ch.current_release || 'unknown' }}</div>
          <a
            v-if="ch.manifest_url"
            :href="ch.manifest_url"
            target="_blank"
            rel="noopener noreferrer"
            class="inline-flex items-center gap-1 text-[11px] text-primary hover:underline"
          >
            View manifest <ExternalLink class="h-2.5 w-2.5" />
          </a>
        </div>
        <div class="rounded-lg border bg-muted/20 p-3 space-y-1">
          <div class="text-xs font-medium text-muted-foreground">Known versions</div>
          <div class="text-sm font-semibold">{{ data.total_versions }}</div>
          <div class="text-[11px] text-muted-foreground">
            Generated {{ formatDateTime(data.generated_at) }}
          </div>
        </div>
      </div>

      <div v-if="data.warnings?.length" class="rounded-md border border-amber-200 bg-amber-50 p-3 text-xs text-amber-900 space-y-1">
        <div class="flex items-center gap-1.5 font-medium">
          <AlertTriangle class="h-3.5 w-3.5" /> Partial release data
        </div>
        <div v-for="(w, i) in data.warnings" :key="i" class="font-mono text-[11px]">{{ w }}</div>
      </div>

      <!-- Storage resolution summary -->
      <div v-if="showStorage !== false && data.storage?.length" class="rounded-lg border bg-card p-4 space-y-2">
        <div class="flex items-center justify-between gap-2">
          <div class="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
            FLUTTER_STORAGE_BASE_URL Resolution
          </div>
          <UiBadge variant="secondary" class="font-mono text-[10px]">{{ data.storage_name }}</UiBadge>
        </div>
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-2">
          <div
            v-for="cat in data.storage"
            :key="cat.name"
            class="rounded-md border bg-muted/10 p-2.5 text-xs space-y-1"
          >
            <div class="flex items-center justify-between gap-2">
              <span class="font-mono font-medium">{{ cat.name }}</span>
              <UiBadge :variant="cat.enabled ? 'success' : 'secondary'">
                {{ cat.enabled ? 'Served' : 'Disabled' }}
              </UiBadge>
            </div>
            <div class="text-[11px] text-muted-foreground break-all">
              Upstream: <span class="font-mono text-foreground">{{ cat.upstream_url }}</span>
              <span v-if="cat.inherits_upstream" class="ml-1 text-[10px] text-muted-foreground">(inherited)</span>
            </div>
            <div class="text-[11px] text-muted-foreground break-all">
              Mirror: <span class="font-mono text-foreground">{{ cat.mirror_url }}</span>
            </div>
            <div class="flex flex-wrap gap-1 pt-0.5">
              <span
                v-for="p in cat.allowed_prefixes"
                :key="p"
                class="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px]"
              >
                /{{ p }}
              </span>
              <span class="rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
                manifest TTL {{ cat.manifest_ttl_seconds }}s
              </span>
            </div>
          </div>
        </div>
      </div>

      <!-- Filters -->
      <div class="flex flex-wrap items-center gap-2">
        <form class="relative flex-1 min-w-[220px] max-w-md" @submit.prevent>
          <Search class="absolute left-3 top-2.5 h-4 w-4 text-muted-foreground" />
          <UiInput v-model="search" placeholder="Filter by version, Dart SDK, or commit hash..." class="pl-9" />
        </form>
        <UiSelect v-model="channelFilter">
          <UiSelectTrigger class="w-40" aria-label="Filter by channel">
            <UiSelectValue placeholder="All Channels" />
          </UiSelectTrigger>
          <UiSelectContent>
            <UiSelectItem v-for="opt in channelOptions" :key="opt.value" :value="opt.value">
              {{ opt.label }}
            </UiSelectItem>
          </UiSelectContent>
        </UiSelect>
        <span class="text-xs text-muted-foreground">
          {{ filtered.length }} of {{ data.total_versions }} versions
        </span>
      </div>

      <!-- Version table -->
      <div class="overflow-x-auto rounded-lg border bg-card">
        <table class="w-full text-sm">
          <thead>
            <tr class="border-b bg-muted/40 text-left text-xs font-medium text-muted-foreground">
              <th class="px-4 py-3">Flutter Version</th>
              <th class="px-4 py-3">Channel</th>
              <th class="px-4 py-3">Dart SDK</th>
              <th class="px-4 py-3">Released</th>
              <th class="px-4 py-3">Platforms</th>
              <th class="px-4 py-3 text-right">Details</th>
            </tr>
          </thead>
          <tbody class="divide-y">
            <template v-for="v in filtered" :key="v.version">
              <tr class="hover:bg-muted/20 transition-colors">
                <td class="px-4 py-3 font-mono text-xs font-medium">
                  {{ v.version }}
                  <UiBadge v-if="v.archived" variant="secondary" class="ml-1.5 text-[10px]">archived</UiBadge>
                </td>
                <td class="px-4 py-3">
                  <UiBadge variant="info" class="capitalize">{{ v.channel || 'unknown' }}</UiBadge>
                </td>
                <td class="px-4 py-3 font-mono text-xs text-muted-foreground">
                  {{ v.dart_sdk_version || '—' }}
                </td>
                <td class="px-4 py-3 text-xs text-muted-foreground whitespace-nowrap">
                  {{ v.release_date || '—' }}
                </td>
                <td class="px-4 py-3">
                  <div class="flex flex-wrap gap-1">
                    <span
                      v-for="d in v.downloads"
                      :key="d.archive"
                      class="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px]"
                    >
                      {{ d.platform }}
                    </span>
                  </div>
                </td>
                <td class="px-4 py-3 text-right">
                  <button
                    type="button"
                    class="inline-flex items-center gap-1 rounded-md border px-2.5 py-1 text-xs font-medium text-primary hover:bg-muted"
                    @click="toggleExpand(v.version)"
                  >
                    {{ expanded === v.version ? 'Hide' : 'Storage & URLs' }}
                  </button>
                </td>
              </tr>
              <tr v-if="expanded === v.version" class="bg-muted/10">
                <td colspan="6" class="px-4 py-4 space-y-3">
                  <div class="grid grid-cols-1 lg:grid-cols-2 gap-3">
                    <div class="rounded-md border bg-card p-3 space-y-1.5">
                      <div class="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
                        Downloads
                      </div>
                      <div v-for="d in v.downloads" :key="d.archive" class="text-xs space-y-0.5">
                        <div class="font-mono text-[11px] break-all">{{ d.archive }}</div>
                        <div class="flex flex-wrap items-center gap-2">
                          <a
                            :href="d.mirror_url"
                            target="_blank"
                            rel="noopener noreferrer"
                            class="inline-flex items-center gap-1 text-[11px] text-primary hover:underline"
                          >
                            <Download class="h-2.5 w-2.5" /> Mirror URL
                          </a>
                          <span v-if="d.sha256" class="font-mono text-[10px] text-muted-foreground">
                            sha256 {{ d.sha256.slice(0, 16) }}…
                          </span>
                        </div>
                      </div>
                      <div v-if="v.hash" class="text-[11px] text-muted-foreground font-mono pt-1">
                        commit: {{ v.hash }}
                      </div>
                    </div>

                    <div class="rounded-md border bg-card p-3 space-y-1.5">
                      <div class="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
                        Storage Resolution
                      </div>
                      <div v-for="st in v.storage" :key="st.path" class="text-xs space-y-0.5 pb-1">
                        <div class="flex items-center gap-1.5">
                          <UiBadge variant="secondary" class="text-[10px]">{{ st.category }}</UiBadge>
                          <span
                            v-if="st.inherited_from"
                            class="text-[10px] text-muted-foreground"
                          >
                            inherits {{ st.inherited_from }} upstream
                          </span>
                        </div>
                        <div class="font-mono text-[11px] break-all">{{ st.mirror_url }}</div>
                        <div class="font-mono text-[10px] text-muted-foreground break-all">
                          upstream → {{ st.upstream_url }}
                        </div>
                      </div>
                      <button
                        type="button"
                        class="inline-flex items-center gap-1 text-[11px] text-primary hover:underline"
                        @click="copySnippet(v.version, v.registry_entry_url)"
                      >
                        <Check v-if="copiedKey === v.version" class="h-2.5 w-2.5" />
                        <Copy v-else class="h-2.5 w-2.5" />
                        Copy registry entry URL
                      </button>
                    </div>
                  </div>
                </td>
              </tr>
            </template>
            <tr v-if="filtered.length === 0">
              <td colspan="6" class="px-4 py-10 text-center text-sm text-muted-foreground">
                No Flutter versions match the current filters.
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
  </div>
</template>
