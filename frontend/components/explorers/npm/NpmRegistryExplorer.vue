<script setup lang="ts">
import { Search, Copy, Check, Package, Layers, Download, ArrowRight, Terminal, Box } from 'lucide-vue-next'
import type { PublicRegistryView } from '~/types/registry'
import { formatRelative, copyText } from '~/utils/format'

const props = defineProps<{
  registry: PublicRegistryView
}>()

interface NpmPackageItem {
  id: string
  name: string
  description?: string
  origin: 'hosted' | 'cached' | 'mixed'
  latest_version?: string
  version_count: number
  downloads: number
  total_size_bytes: number
  updated_at?: string
}

interface NpmListResponse {
  packages: NpmPackageItem[]
  total: number
  page: number
  limit: number
}

const searchQuery = ref('')
const page = ref(1)
const limit = 15
const loading = ref(true)
const errorMsg = ref('')
const packages = ref<NpmPackageItem[]>([])
const total = ref(0)
const copied = ref(false)

const registryUrl = computed(() => {
  if (typeof window === 'undefined') return props.registry.base_path
  return `${window.location.origin}${props.registry.base_path}`
})

async function loadPackages() {
  loading.value = true
  errorMsg.value = ''
  try {
    const params = new URLSearchParams({ page: String(page.value), limit: String(limit) })
    if (searchQuery.value.trim()) params.set('q', searchQuery.value.trim())
    const res = await apiFetch<NpmListResponse>(
      `${props.registry.base_path}/api/packages?${params.toString()}`,
    )
    packages.value = res.packages || []
    total.value = res.total || 0
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to load npm packages'
  } finally {
    loading.value = false
  }
}

function handleSearch() {
  page.value = 1
  loadPackages()
}

async function copy(text: string) {
  if (await copyText(text)) {
    copied.value = true
    setTimeout(() => (copied.value = false), 2000)
  }
}

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / limit)))

function goToPage(next: number) {
  if (next < 1 || next > totalPages.value) return
  page.value = next
  loadPackages()
}

onMounted(loadPackages)
watch(() => props.registry.slug, () => {
  page.value = 1
  loadPackages()
})
</script>

<template>
  <div>
    <section class="bg-[#1a1010] text-white border-b border-red-950">
      <div class="mx-auto max-w-6xl px-4 sm:px-6 py-10 sm:py-12">
        <div class="flex flex-wrap items-center gap-2 text-xs text-red-200 mb-3">
          <span class="inline-flex items-center gap-1 rounded-full bg-red-500/15 border border-red-400/25 px-2.5 py-0.5 font-medium">
            <Box class="h-3 w-3" />
            npm Packages
          </span>
          <span class="rounded-full bg-white/10 px-2.5 py-0.5 capitalize">{{ registry.mode }}</span>
          <span class="rounded-full bg-white/10 px-2.5 py-0.5 capitalize">{{ registry.visibility }}</span>
          <span v-if="registry.is_default" class="rounded-full bg-emerald-500/20 text-emerald-300 px-2.5 py-0.5">
            Default Instance
          </span>
        </div>

        <div class="flex flex-col lg:flex-row lg:items-end justify-between gap-6">
          <div>
            <h1 class="text-2xl sm:text-3xl font-bold tracking-tight">{{ registry.name }}</h1>
            <p class="mt-1.5 text-sm text-red-100/80 max-w-2xl">
              {{ registry.description || 'Self-hosted npm registry with packuments, scoped packages and tarball caching.' }}
            </p>
          </div>
          <button
            type="button"
            class="inline-flex items-center gap-2.5 rounded-lg bg-slate-900/90 border border-slate-700 px-3.5 py-2 text-xs font-mono text-red-200 hover:border-red-400/60 transition-colors text-left"
            title="Copy the npm registry URL"
            @click="copy(registryUrl)"
          >
            <Terminal class="h-4 w-4 shrink-0 text-red-300" />
            <span class="truncate">{{ registryUrl }}</span>
            <Check v-if="copied" class="h-4 w-4 shrink-0 text-emerald-400" />
            <Copy v-else class="h-4 w-4 shrink-0 text-slate-400" />
          </button>
        </div>
      </div>
    </section>

    <section class="mx-auto max-w-6xl px-4 sm:px-6 py-8 space-y-5">
      <div class="rounded-xl border bg-card p-4">
        <h2 class="text-sm font-semibold text-foreground">Use this registry</h2>
        <p class="mt-1 text-xs text-muted-foreground">
          Point npm, yarn or pnpm at this URL, then install packages normally:
        </p>
        <div class="mt-3 grid grid-cols-1 sm:grid-cols-2 gap-2">
          <div class="relative rounded-lg bg-slate-900 p-3.5 pr-10 font-mono text-xs text-slate-100">
            <code>npm install --registry={{ registryUrl }} lodash</code>
            <button type="button" class="absolute right-2.5 top-2.5 rounded p-1 text-slate-400 hover:text-white"
              aria-label="Copy npm install command" @click="copy(`npm install --registry=${registryUrl} lodash`)">
              <Check v-if="copied" class="h-3.5 w-3.5 text-emerald-400" />
              <Copy v-else class="h-3.5 w-3.5" />
            </button>
          </div>
          <div class="relative rounded-lg bg-slate-900 p-3.5 pr-10 font-mono text-xs text-slate-100">
            <code>npm config set registry {{ registryUrl }}</code>
            <button type="button" class="absolute right-2.5 top-2.5 rounded p-1 text-slate-400 hover:text-white"
              aria-label="Copy npm config command" @click="copy(`npm config set registry ${registryUrl}`)">
              <Check v-if="copied" class="h-3.5 w-3.5 text-emerald-400" />
              <Copy v-else class="h-3.5 w-3.5" />
            </button>
          </div>
        </div>
      </div>

      <form class="relative" @submit.prevent="handleSearch">
        <Search class="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground pointer-events-none" />
        <input v-model="searchQuery" type="search" placeholder="Search npm packages by name…"
          class="h-9 w-full rounded-md border border-input bg-background pl-9 pr-3 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" />
      </form>

      <SharedErrorBanner v-if="errorMsg" :message="errorMsg" />
      <div v-if="loading" class="rounded-xl border bg-card animate-pulse h-40" />

      <div v-else-if="packages.length === 0" class="rounded-xl border bg-card">
        <SharedEmptyState title="No npm packages found"
          description="Nothing has been mirrored into this registry yet. Install a package through it to populate the cache." />
      </div>

      <div v-else class="grid grid-cols-1 md:grid-cols-2 gap-3">
        <NuxtLink v-for="p in packages" :key="p.id"
          :to="`/explore/${registry.slug}/packages/${encodeURIComponent(p.name)}`"
          class="group rounded-xl border bg-card p-4 transition-colors hover:border-red-500">
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0">
              <h3 class="font-mono text-sm font-semibold text-foreground truncate group-hover:text-red-700">
                {{ p.name }}
              </h3>
              <p v-if="p.description" class="mt-1 text-xs text-muted-foreground line-clamp-2">{{ p.description }}</p>
            </div>
            <ArrowRight class="h-4 w-4 shrink-0 text-muted-foreground group-hover:text-red-600" />
          </div>
          <div class="mt-3 flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
            <span v-if="p.latest_version" class="inline-flex items-center gap-1 font-mono">
              <Package class="h-3 w-3" /> {{ p.latest_version }}
            </span>
            <span class="inline-flex items-center gap-1">
              <Layers class="h-3 w-3" /> {{ p.version_count }} version{{ p.version_count === 1 ? '' : 's' }}
            </span>
            <span class="inline-flex items-center gap-1"><Download class="h-3 w-3" /> {{ p.downloads }}</span>
            <span v-if="p.updated_at">{{ formatRelative(p.updated_at) }}</span>
          </div>
        </NuxtLink>
      </div>

      <SharedPaginationBar v-if="!loading && packages.length > 0" :page="page" :limit="limit" :total="total"
        @change="goToPage" />
    </section>
  </div>
</template>
