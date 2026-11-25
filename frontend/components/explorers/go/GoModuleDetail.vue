<script setup lang="ts">
import { Copy, Check, Terminal, Package, Layers, Clock, Download, FileCode } from 'lucide-vue-next'
import type { PublicRegistryView } from '~/types/registry'
import { formatRelative, formatDate, formatBytes, copyText } from '~/utils/format'

const props = defineProps<{
  registry: PublicRegistryView
  packageName: string
}>()

interface GoVersionItem {
  version: string
  info_time?: string
  published_at?: string
  zip_size?: number
  zip_cached?: boolean
}

interface GoModuleDetail {
  id: string
  path: string
  origin: 'hosted' | 'cached'
  description?: string
  latest_version?: string
  version_count: number
  total_size_bytes?: number
  downloads: number
  updated_at: string
  versions: GoVersionItem[]
}

const loading = ref(true)
const errorMsg = ref('')
const detail = ref<GoModuleDetail | null>(null)
const copiedKey = ref('')

const hostedUrl = computed(() => {
  if (typeof window === 'undefined') return props.registry.base_path
  return `${window.location.origin}${props.registry.base_path}`
})

const proxyUrl = computed(() => hostedUrl.value)

async function loadDetail() {
  loading.value = true
  errorMsg.value = ''
  try {
    detail.value = await apiFetch<GoModuleDetail>(
      `${props.registry.base_path}/api/modules/${encodeURIComponent(props.packageName)}`,
    )
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to load Go module details'
  } finally {
    loading.value = false
  }
}

async function copySnippet(key: string, text: string) {
  if (await copyText(text)) {
    copiedKey.value = key
    setTimeout(() => {
      if (copiedKey.value === key) copiedKey.value = ''
    }, 2000)
  }
}

onMounted(loadDetail)
watch(() => [props.registry.slug, props.packageName], loadDetail)
</script>

<template>
  <div class="mx-auto max-w-6xl px-4 sm:px-6 py-8">
    <div class="mb-5">
      <NuxtLink
        :to="`/explore/${registry.slug}`"
        class="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground transition-colors"
      >
        ← Back to {{ registry.name }}
      </NuxtLink>
    </div>

    <div v-if="loading" class="rounded-xl border bg-card animate-pulse h-56" />

    <SharedErrorBanner v-else-if="errorMsg" :message="errorMsg" />

    <div v-else-if="detail" class="space-y-6">
      <!-- Module header -->
      <UiCard class="p-6">
        <div class="flex flex-col md:flex-row md:items-start justify-between gap-4">
          <div class="space-y-2 min-w-0">
            <div class="flex flex-wrap items-center gap-2.5">
              <h1 class="text-2xl sm:text-3xl font-bold tracking-tight text-slate-900 font-mono">
                {{ detail.path }}
              </h1>
              <span
                v-if="detail.latest_version"
                class="rounded-md bg-cyan-500/10 text-cyan-700 px-2.5 py-0.5 font-mono text-sm font-semibold"
              >
                {{ detail.latest_version }}
              </span>
              <UiBadge :variant="detail.origin === 'hosted' ? 'info' : 'secondary'">
                {{ detail.origin === 'hosted' ? 'Hosted Locally' : 'Cached Upstream' }}
              </UiBadge>
            </div>
            <p v-if="detail.description" class="text-sm text-slate-600 max-w-2xl">
              {{ detail.description }}
            </p>
            <div class="flex flex-wrap items-center gap-3 text-xs text-muted-foreground pt-1">
              <span>Proxy: <strong class="font-mono text-foreground">{{ proxyUrl }}</strong></span>
              <span>•</span>
              <span>Updated {{ formatDate(detail.updated_at) }}</span>
            </div>
          </div>

          <div class="flex flex-col gap-2 shrink-0">
            <button
              type="button"
              class="inline-flex items-center justify-between gap-3 rounded-lg border bg-slate-900 text-slate-100 px-3.5 py-2 text-xs font-mono hover:bg-slate-800 transition-colors"
              title="Copy the GOPROXY URL"
              @click="copySnippet('proxy', proxyUrl)"
            >
              <span class="truncate max-w-[16rem]">GOPROXY={{ proxyUrl }}</span>
              <Check v-if="copiedKey === 'proxy'" class="h-3.5 w-3.5 text-emerald-400 shrink-0" />
              <Copy v-else class="h-3.5 w-3.5 text-slate-400 shrink-0" />
            </button>
            <button
              type="button"
              class="inline-flex items-center justify-between gap-3 rounded-lg border bg-slate-900 text-slate-100 px-3.5 py-2 text-xs font-mono hover:bg-slate-800 transition-colors"
              title="Copy the go get command"
              @click="copySnippet('get', `GOPROXY=${proxyUrl} go get ${detail.path}@${detail.latest_version || 'latest'}`)"
            >
              <span class="truncate max-w-[16rem]">go get {{ detail.path }}@{{ detail.latest_version || 'latest' }}</span>
              <Check v-if="copiedKey === 'get'" class="h-3.5 w-3.5 text-emerald-400 shrink-0" />
              <Copy v-else class="h-3.5 w-3.5 text-slate-400 shrink-0" />
            </button>
          </div>
        </div>
      </UiCard>

      <!-- Summary cards -->
      <div class="grid grid-cols-2 lg:grid-cols-4 gap-3">
        <UiCard class="p-4">
          <div class="flex items-center gap-2 text-xs text-muted-foreground">
            <Package class="h-3.5 w-3.5" />
            Latest Version
          </div>
          <p class="mt-1.5 font-mono text-sm font-semibold text-foreground truncate">
            {{ detail.latest_version || '—' }}
          </p>
        </UiCard>
        <UiCard class="p-4">
          <div class="flex items-center gap-2 text-xs text-muted-foreground">
            <Layers class="h-3.5 w-3.5" />
            Versions
          </div>
          <p class="mt-1.5 font-mono text-sm font-semibold text-foreground">{{ detail.version_count }}</p>
        </UiCard>
        <UiCard class="p-4">
          <div class="flex items-center gap-2 text-xs text-muted-foreground">
            <Download class="h-3.5 w-3.5" />
            Downloads
          </div>
          <p class="mt-1.5 font-mono text-sm font-semibold text-foreground">{{ detail.downloads }}</p>
        </UiCard>
        <UiCard class="p-4">
          <div class="flex items-center gap-2 text-xs text-muted-foreground">
            <FileCode class="h-3.5 w-3.5" />
            Cached Size
          </div>
          <p class="mt-1.5 font-mono text-sm font-semibold text-foreground">
            {{ formatBytes(detail.total_size_bytes) }}
          </p>
        </UiCard>
      </div>

      <!-- Versions table -->
      <UiCard class="overflow-hidden">
        <div class="border-b px-5 py-4">
          <h2 class="text-base font-semibold text-foreground">Versions</h2>
          <p class="mt-0.5 text-xs text-muted-foreground">
            Each version is served over the standard GOPROXY protocol.
          </p>
        </div>
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead class="bg-muted/40 text-xs uppercase tracking-wide text-muted-foreground">
              <tr>
                <th class="px-5 py-2.5 text-left font-medium">Version</th>
                <th class="px-5 py-2.5 text-left font-medium">Published</th>
                <th class="px-5 py-2.5 text-left font-medium">Size</th>
                <th class="px-5 py-2.5 text-left font-medium">Cached</th>
                <th class="px-5 py-2.5 text-right font-medium">Install</th>
              </tr>
            </thead>
            <tbody class="divide-y">
              <tr v-for="v in detail.versions" :key="v.version" class="hover:bg-muted/30">
                <td class="px-5 py-2.5 font-mono text-xs font-medium text-foreground">{{ v.version }}</td>
                <td class="px-5 py-2.5 text-xs text-muted-foreground">
                  {{ formatRelative(v.info_time || v.published_at) }}
                </td>
                <td class="px-5 py-2.5 text-xs font-mono text-muted-foreground">
                  {{ formatBytes(v.zip_size) }}
                </td>
                <td class="px-5 py-2.5 text-xs">
                  <UiBadge :variant="v.zip_cached ? 'success' : 'secondary'">
                    {{ v.zip_cached ? 'Yes' : 'No' }}
                  </UiBadge>
                </td>
                <td class="px-5 py-2.5 text-right">
                  <button
                    type="button"
                    class="inline-flex items-center gap-1.5 rounded-md border bg-background px-2.5 py-1 text-xs font-mono text-foreground hover:bg-muted transition-colors"
                    @click="copySnippet('v-' + v.version, `GOPROXY=${proxyUrl} go get ${detail.path}@${v.version}`)"
                  >
                    <Terminal class="h-3 w-3" />
                    go get
                    <Check v-if="copiedKey === 'v-' + v.version" class="h-3 w-3 text-emerald-500" />
                    <Copy v-else class="h-3 w-3 text-muted-foreground" />
                  </button>
                </td>
              </tr>
              <tr v-if="!detail.versions || detail.versions.length === 0">
                <td colspan="5" class="px-5 py-8 text-center text-sm text-muted-foreground">
                  No versions have been mirrored for this module yet.
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </UiCard>
    </div>
  </div>
</template>
