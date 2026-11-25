<script setup lang="ts">
import { Search, Copy, Check, Package, Layers, Download, ArrowRight, Terminal } from 'lucide-vue-next'
import type { PublicRegistryView } from '~/types/registry'
import { formatRelative, copyText } from '~/utils/format'

const props = defineProps<{
  registry: PublicRegistryView
}>()

interface GoModuleItem {
  id: string
  path: string
  origin: 'hosted' | 'cached'
  description?: string
  latest_version?: string
  version_count: number
  downloads: number
  updated_at: string
}

interface GoListResponse {
  modules: GoModuleItem[]
  total: number
  page: number
  limit: number
}

const searchQuery = ref('')
const page = ref(1)
const limit = 15
const loading = ref(true)
const errorMsg = ref('')
const modules = ref<GoModuleItem[]>([])
const total = ref(0)
const copiedUrl = ref(false)

const hostedUrl = computed(() => {
  if (typeof window === 'undefined') return props.registry.base_path
  return `${window.location.origin}${props.registry.base_path}`
})

const goProxyUrl = computed(() => hostedUrl.value)

async function loadModules() {
  loading.value = true
  errorMsg.value = ''
  try {
    const params = new URLSearchParams({
      page: String(page.value),
      limit: String(limit),
    })
    if (searchQuery.value.trim()) params.set('q', searchQuery.value.trim())

    const res = await apiFetch<GoListResponse>(
      `${props.registry.base_path}/api/modules?${params.toString()}`,
    )
    modules.value = res.modules || []
    total.value = res.total || 0
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to load Go modules'
  } finally {
    loading.value = false
  }
}

function handleSearch() {
  page.value = 1
  loadModules()
}

async function copyProxyUrl() {
  if (await copyText(goProxyUrl.value)) {
    copiedUrl.value = true
    setTimeout(() => (copiedUrl.value = false), 2000)
  }
}

async function copyGoGet() {
  if (await copyText(`GOPROXY=${goProxyUrl.value} go get ${'github.com/pkg/errors@latest'}`)) {
    copiedUrl.value = true
    setTimeout(() => (copiedUrl.value = false), 2000)
  }
}

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / limit)))

function goToPage(p: number) {
  if (p < 1 || p > totalPages.value) return
  page.value = p
  loadModules()
}

onMounted(loadModules)
watch(() => props.registry.slug, () => {
  page.value = 1
  loadModules()
})
</script>

<template>
  <div>
    <!-- Go-flavored hero banner -->
    <section class="bg-[#0b1b2b] text-white border-b border-slate-800">
      <div class="mx-auto max-w-6xl px-4 sm:px-6 py-10 sm:py-12">
        <div class="flex flex-wrap items-center gap-2 text-xs text-cyan-300 mb-3">
          <span class="inline-flex items-center gap-1 rounded-full bg-cyan-500/15 border border-cyan-400/25 px-2.5 py-0.5 font-medium">
            Go Modules
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
            <p class="mt-1.5 text-sm text-slate-300 max-w-2xl">
              {{ registry.description || 'Self-hosted Go module proxy (GOPROXY protocol).' }}
            </p>
          </div>

          <button
            type="button"
            class="inline-flex items-center gap-2.5 rounded-lg bg-slate-900/90 border border-slate-700 px-3.5 py-2 text-xs font-mono text-cyan-200 hover:border-cyan-400/60 transition-colors text-left"
            title="Copy the GOPROXY URL"
            @click="copyProxyUrl"
          >
            <Terminal class="h-4 w-4 shrink-0 text-cyan-300" />
            <span class="truncate">{{ goProxyUrl }}</span>
            <Check v-if="copiedUrl" class="h-4 w-4 shrink-0 text-emerald-400" />
            <Copy v-else class="h-4 w-4 shrink-0 text-slate-400" />
          </button>
        </div>
      </div>
    </section>

    <section class="mx-auto max-w-6xl px-4 sm:px-6 py-8 space-y-5">
      <!-- Setup snippet -->
      <div class="rounded-xl border bg-card p-4">
        <h2 class="text-sm font-semibold text-foreground">Use this proxy</h2>
        <p class="mt-1 text-xs text-muted-foreground">
          Point the go command at this mirror, then fetch modules normally:
        </p>
        <div class="mt-3 grid grid-cols-1 sm:grid-cols-2 gap-2">
          <div class="relative rounded-lg bg-slate-900 p-3.5 pr-10 font-mono text-xs text-slate-100">
            <code>export GOPROXY={{ goProxyUrl }}</code>
            <button
              type="button"
              class="absolute right-2.5 top-2.5 rounded p-1 text-slate-400 hover:text-white"
              aria-label="Copy GOPROXY export"
              @click="copyProxyUrl"
            >
              <Check v-if="copiedUrl" class="h-3.5 w-3.5 text-emerald-400" />
              <Copy v-else class="h-3.5 w-3.5" />
            </button>
          </div>
          <div class="relative rounded-lg bg-slate-900 p-3.5 pr-10 font-mono text-xs text-slate-100">
            <code>go get github.com/pkg/errors@latest</code>
            <button
              type="button"
              class="absolute right-2.5 top-2.5 rounded p-1 text-slate-400 hover:text-white"
              aria-label="Copy go get command"
              @click="copyGoGet"
            >
              <Check v-if="copiedUrl" class="h-3.5 w-3.5 text-emerald-400" />
              <Copy v-else class="h-3.5 w-3.5" />
            </button>
          </div>
        </div>
      </div>

      <!-- Search -->
      <div class="flex flex-col sm:flex-row gap-3 sm:items-center">
        <form class="relative flex-1" @submit.prevent="handleSearch">
          <Search class="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground pointer-events-none" />
          <input
            v-model="searchQuery"
            type="search"
            placeholder="Search modules by path…"
            class="h-9 w-full rounded-md border border-input bg-background pl-9 pr-3 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          />
        </form>
      </div>

      <SharedErrorBanner v-if="errorMsg" :message="errorMsg" />

      <div v-if="loading" class="rounded-xl border bg-card animate-pulse h-40" />

      <div v-else-if="modules.length === 0" class="rounded-xl border bg-card">
        <SharedEmptyState
          title="No Go modules found"
          description="Nothing has been mirrored into this proxy yet. Run a go get through it to populate the cache."
        />
      </div>

      <div v-else class="grid grid-cols-1 md:grid-cols-2 gap-3">
        <NuxtLink
          v-for="m in modules"
          :key="m.id"
          :to="`/explore/${registry.slug}/packages/${m.path}`"
          class="group rounded-xl border bg-card p-4 transition-colors hover:border-cyan-300"
        >
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0">
              <h3 class="font-mono text-sm font-semibold text-foreground truncate group-hover:text-cyan-700">
                {{ m.path }}
              </h3>
              <p v-if="m.description" class="mt-1 text-xs text-muted-foreground line-clamp-2">
                {{ m.description }}
              </p>
            </div>
            <ArrowRight class="h-4 w-4 shrink-0 text-muted-foreground group-hover:text-cyan-600" />
          </div>
          <div class="mt-3 flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
            <span v-if="m.latest_version" class="inline-flex items-center gap-1 font-mono">
              <Package class="h-3 w-3" />
              {{ m.latest_version }}
            </span>
            <span class="inline-flex items-center gap-1">
              <Layers class="h-3 w-3" />
              {{ m.version_count }} version{{ m.version_count === 1 ? '' : 's' }}
            </span>
            <span class="inline-flex items-center gap-1">
              <Download class="h-3 w-3" />
              {{ m.downloads }}
            </span>
            <span>{{ formatRelative(m.updated_at) }}</span>
          </div>
        </NuxtLink>
      </div>

      <SharedPaginationBar
        v-if="!loading && modules.length > 0"
        :page="page"
        :limit="limit"
        :total="total"
        @change="goToPage"
      />
    </section>
  </div>
</template>
