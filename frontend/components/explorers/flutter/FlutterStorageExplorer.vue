<script setup lang="ts">
import {
  Search,
  Copy,
  Check,
  Download,
  FileCode2,
  HardDrive,
  ExternalLink,
  RefreshCw,
  Terminal,
} from 'lucide-vue-next'
import type { PublicRegistryView } from '~/types/registry'
import { formatBytes, formatRelative, copyText } from '~/utils/format'

const props = defineProps<{
  registry: PublicRegistryView
}>()

interface FlutterArtifactItem {
  id: string
  path: string
  category: string
  content_type: string
  size_bytes: number
  digest: string
  is_manifest: boolean
  downloads: number
  fetched_at: string
  updated_at: string
}

interface FlutterArtifactListResponse {
  artifacts: FlutterArtifactItem[]
  total: number
  page: number
  limit: number
  allowed_prefixes?: string[]
}

const searchQuery = ref('')
const categoryFilter = ref('')
const page = ref(1)
const limit = 20
const loading = ref(true)
const errorMsg = ref('')
const artifacts = ref<FlutterArtifactItem[]>([])
const total = ref(0)
const allowedPrefixes = ref<string[]>([])
const copiedKey = ref('')
const shellTab = ref<'posix' | 'powershell'>('posix')

const storageBaseUrl = computed(() => {
  if (typeof window === 'undefined') return props.registry.base_path
  return `${window.location.origin}${props.registry.base_path}`
})

const rootBaseUrl = computed(() => {
  if (typeof window === 'undefined') return ''
  return window.location.origin
})

const setupSnippet = computed(() => {
  if (shellTab.value === 'powershell') {
    return `$env:FLUTTER_STORAGE_BASE_URL="${storageBaseUrl.value}"`
  }
  return `export FLUTTER_STORAGE_BASE_URL="${storageBaseUrl.value}"`
})

const categories = [
  { key: '', label: 'All Cached Objects' },
  { key: 'releases', label: 'SDK Releases' },
  { key: 'engine', label: 'Engine & Infra' },
  { key: 'gradle', label: 'Gradle / Maven' },
  { key: 'dart-sdk', label: 'Dart SDK' },
]

const quickManifests = [
  { label: 'Linux Releases JSON', path: 'flutter_infra_release/releases/releases_linux.json' },
  { label: 'macOS Releases JSON', path: 'flutter_infra_release/releases/releases_macos.json' },
  { label: 'Windows Releases JSON', path: 'flutter_infra_release/releases/releases_windows.json' },
]

async function loadArtifacts() {
  loading.value = true
  errorMsg.value = ''
  try {
    const params = new URLSearchParams({
      page: String(page.value),
      limit: String(limit),
    })
    if (searchQuery.value.trim()) params.set('q', searchQuery.value.trim())
    if (categoryFilter.value) params.set('category', categoryFilter.value)

    const res = await apiFetch<FlutterArtifactListResponse>(
      `${props.registry.base_path}/api/artifacts?${params.toString()}`,
    )
    artifacts.value = res.artifacts || []
    total.value = res.total || 0
    allowedPrefixes.value = res.allowed_prefixes || []
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to load Flutter storage artifacts'
  } finally {
    loading.value = false
  }
}

function handleSearch() {
  page.value = 1
  loadArtifacts()
}

function setCategory(cat: string) {
  categoryFilter.value = cat
  page.value = 1
  loadArtifacts()
}

async function copySnippet(key: string, text: string) {
  if (await copyText(text)) {
    copiedKey.value = key
    setTimeout(() => {
      if (copiedKey.value === key) copiedKey.value = ''
    }, 2000)
  }
}

onMounted(loadArtifacts)
watch(
  () => props.registry.slug,
  () => {
    page.value = 1
    loadArtifacts()
  },
)
</script>

<template>
  <div>
    <!-- Hero banner -->
    <section class="bg-[#122030] text-white border-b border-slate-800">
      <div class="mx-auto max-w-6xl px-4 sm:px-6 py-10 sm:py-12">
        <div class="flex flex-wrap items-center gap-2 text-xs text-sky-300 mb-3">
          <span class="inline-flex items-center gap-1 rounded-full bg-sky-500/15 border border-sky-400/25 px-2.5 py-0.5 font-medium">
            Flutter Storage Mirror
          </span>
          <span class="rounded-full bg-white/10 px-2.5 py-0.5 capitalize">{{ registry.mode }}</span>
          <span class="rounded-full bg-white/10 px-2.5 py-0.5 capitalize">{{ registry.visibility }}</span>
          <span v-if="registry.is_default" class="rounded-full bg-emerald-500/20 text-emerald-300 px-2.5 py-0.5">
            Default FLUTTER_STORAGE_BASE_URL
          </span>
        </div>

        <div class="flex flex-col lg:flex-row lg:items-end justify-between gap-6">
          <div class="max-w-2xl">
            <h1 class="text-2xl sm:text-3xl font-bold tracking-tight">{{ registry.name }}</h1>
            <p class="mt-1.5 text-sm text-slate-300">
              {{ registry.description || 'Caching mirror for Flutter SDK releases, engine artifacts, Dart archives, and Gradle Maven dependencies.' }}
            </p>
          </div>

          <button
            type="button"
            class="inline-flex items-center gap-2.5 rounded-lg bg-slate-900/90 border border-slate-700 px-3.5 py-2 text-xs font-mono text-sky-200 hover:border-sky-400/60 transition-colors text-left"
            @click="copySnippet('hero', setupSnippet)"
          >
            <Terminal class="h-3.5 w-3.5 text-sky-400 shrink-0" />
            <span class="truncate max-w-[340px]">{{ setupSnippet }}</span>
            <Check v-if="copiedKey === 'hero'" class="h-3.5 w-3.5 text-emerald-400 shrink-0" />
            <Copy v-else class="h-3.5 w-3.5 text-slate-400 shrink-0" />
          </button>
        </div>

        <!-- Search bar -->
        <form class="mt-6 max-w-2xl flex items-center gap-2" @submit.prevent="handleSearch">
          <div class="relative flex-1">
            <Search class="absolute left-3.5 top-3 h-4 w-4 text-slate-400" />
            <input
              v-model="searchQuery"
              type="text"
              placeholder="Filter cached storage paths (e.g. releases_linux.json, flutter_engine, .jar)..."
              class="w-full rounded-lg bg-slate-900/90 border border-slate-700 pl-10 pr-4 py-2.5 text-sm text-white placeholder:text-slate-400 focus:outline-none focus:border-sky-400"
            />
          </div>
          <UiButton type="submit" class="bg-[#0175c2] hover:bg-[#0284d9] text-white h-[42px]">
            Filter
          </UiButton>
        </form>
      </div>
    </section>

    <!-- Main Content -->
    <section class="mx-auto max-w-6xl px-4 sm:px-6 py-8 space-y-6">
      <!-- Setup & Quick Manifests row -->
      <div class="grid grid-cols-1 lg:grid-cols-3 gap-4">
        <UiCard class="lg:col-span-2 p-5 space-y-3">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <div>
              <h2 class="text-sm font-semibold">Client Setup (FLUTTER_STORAGE_BASE_URL)</h2>
              <p class="text-xs text-muted-foreground">
                Point the Flutter CLI and Gradle engine downloads to this mirror. Release manifests automatically rewrite <code class="font-mono">base_url</code> to this server.
              </p>
            </div>
            <div class="inline-flex rounded-md border bg-muted/40 p-0.5 text-xs">
              <button
                type="button"
                :class="[
                  'rounded px-2.5 py-1 font-medium transition-colors',
                  shellTab === 'posix' ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground',
                ]"
                @click="shellTab = 'posix'"
              >
                macOS / Linux
              </button>
              <button
                type="button"
                :class="[
                  'rounded px-2.5 py-1 font-medium transition-colors',
                  shellTab === 'powershell' ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground',
                ]"
                @click="shellTab = 'powershell'"
              >
                PowerShell
              </button>
            </div>
          </div>

          <div class="relative rounded-lg bg-slate-950 text-slate-100 p-3.5 font-mono text-xs overflow-x-auto">
            <pre>{{ setupSnippet }}</pre>
            <button
              type="button"
              class="absolute right-2.5 top-2.5 inline-flex items-center gap-1 rounded bg-slate-800 px-2 py-1 text-[11px] text-slate-200 hover:bg-slate-700"
              @click="copySnippet('box', setupSnippet)"
            >
              <Check v-if="copiedKey === 'box'" class="h-3 w-3 text-emerald-400" />
              <Copy v-else class="h-3 w-3" />
              {{ copiedKey === 'box' ? 'Copied' : 'Copy' }}
            </button>
          </div>

          <p v-if="registry.is_default && rootBaseUrl" class="text-[11px] text-muted-foreground">
            Root compatibility is active for this default instance: you may also set
            <code class="font-mono text-foreground">FLUTTER_STORAGE_BASE_URL={{ rootBaseUrl }}</code>.
          </p>
        </UiCard>

        <UiCard class="p-5 space-y-3">
          <h2 class="text-sm font-semibold">Quick Release Manifests</h2>
          <p class="text-xs text-muted-foreground">
            Open any manifest URL below to trigger on-demand upstream fetch, URL rewriting, and caching.
          </p>
          <div class="space-y-2">
            <a
              v-for="m in quickManifests"
              :key="m.path"
              :href="`${registry.base_path}/${m.path}`"
              target="_blank"
              rel="noopener noreferrer"
              class="flex items-center justify-between rounded-md border px-3 py-2 text-xs font-medium hover:border-primary/50 hover:bg-muted/30 transition-colors"
            >
              <span class="inline-flex items-center gap-2">
                <FileCode2 class="h-3.5 w-3.5 text-primary" />
                {{ m.label }}
              </span>
              <ExternalLink class="h-3.5 w-3.5 text-muted-foreground" />
            </a>
          </div>
          <div v-if="allowedPrefixes.length > 0" class="pt-1 border-t">
            <div class="text-[11px] text-muted-foreground mb-1">Allowed Storage Prefixes</div>
            <div class="flex flex-wrap gap-1">
              <span
                v-for="p in allowedPrefixes"
                :key="p"
                class="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-foreground"
              >
                /{{ p }}
              </span>
            </div>
          </div>
        </UiCard>
      </div>

      <!-- Category filter pills & Refresh -->
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="flex flex-wrap items-center gap-1.5">
          <button
            v-for="cat in categories"
            :key="cat.key"
            type="button"
            :class="[
              'rounded-full px-3 py-1 text-xs font-medium transition-colors border',
              categoryFilter === cat.key
                ? 'bg-primary text-primary-foreground border-primary'
                : 'bg-card text-muted-foreground border-border hover:text-foreground',
            ]"
            @click="setCategory(cat.key)"
          >
            {{ cat.label }}
          </button>
        </div>

        <div class="flex items-center gap-3 text-xs text-muted-foreground">
          <span>{{ total }} cached object{{ total === 1 ? '' : 's' }}</span>
          <UiButton variant="outline" size="sm" @click="loadArtifacts">
            <RefreshCw class="h-3.5 w-3.5" />
            Refresh
          </UiButton>
        </div>
      </div>

      <SharedErrorBanner :message="errorMsg" />

      <!-- Cached Storage Artifacts Table -->
      <div class="overflow-x-auto rounded-lg border bg-card">
        <table class="w-full text-sm">
          <thead>
            <tr class="border-b bg-muted/40 text-left text-xs font-medium text-muted-foreground">
              <th class="px-4 py-3">Storage Path</th>
              <th class="px-4 py-3">Category</th>
              <th class="px-4 py-3">Size</th>
              <th class="px-4 py-3">SHA-256</th>
              <th class="px-4 py-3">Downloads</th>
              <th class="px-4 py-3">Cached</th>
              <th class="px-4 py-3 text-right">Download</th>
            </tr>
          </thead>
          <tbody class="divide-y">
            <tr v-if="loading">
              <td colspan="7" class="px-4 py-10 text-center text-muted-foreground">
                Loading cached Flutter storage artifacts...
              </td>
            </tr>
            <tr v-else-if="artifacts.length === 0">
              <td colspan="7" class="px-4 py-12 text-center">
                <div class="flex flex-col items-center gap-2 text-muted-foreground">
                  <HardDrive class="h-7 w-7 opacity-40" />
                  <p class="text-sm font-medium text-foreground">No cached Flutter artifacts yet</p>
                  <p class="text-xs max-w-md">
                    Artifacts are cached automatically on first request when Flutter downloads SDK archives, engine binaries, or release manifests through this mirror.
                  </p>
                </div>
              </td>
            </tr>
            <tr v-for="item in artifacts" :key="item.id" class="hover:bg-muted/20 transition-colors">
              <td class="px-4 py-3">
                <div class="font-mono text-xs text-foreground break-all">{{ item.path }}</div>
                <div class="text-[11px] text-muted-foreground mt-0.5">{{ item.content_type }}</div>
              </td>
              <td class="px-4 py-3 whitespace-nowrap">
                <div class="flex items-center gap-1.5">
                  <UiBadge variant="secondary" class="capitalize">{{ item.category || 'storage' }}</UiBadge>
                  <UiBadge v-if="item.is_manifest" variant="info">Manifest</UiBadge>
                </div>
              </td>
              <td class="px-4 py-3 text-xs text-muted-foreground whitespace-nowrap">
                {{ formatBytes(item.size_bytes) }}
              </td>
              <td class="px-4 py-3 font-mono text-xs text-muted-foreground whitespace-nowrap">
                {{ item.digest ? item.digest.slice(0, 12) + '…' : '—' }}
              </td>
              <td class="px-4 py-3 text-xs text-muted-foreground whitespace-nowrap">
                <span class="inline-flex items-center gap-1">
                  <Download class="h-3 w-3" />
                  {{ item.downloads }}
                </span>
              </td>
              <td class="px-4 py-3 text-xs text-muted-foreground whitespace-nowrap">
                {{ formatRelative(item.updated_at) }}
              </td>
              <td class="px-4 py-3 text-right whitespace-nowrap">
                <a
                  :href="`${registry.base_path}/${item.path}`"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="inline-flex items-center gap-1 rounded-md border px-2.5 py-1 text-xs font-medium text-primary hover:bg-muted transition-colors"
                >
                  <Download class="h-3 w-3" />
                  Fetch
                </a>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <SharedPaginationBar
        :page="page"
        :limit="limit"
        :total="total"
        @change="(p) => { page = p; loadArtifacts() }"
      />
    </section>
  </div>
</template>
