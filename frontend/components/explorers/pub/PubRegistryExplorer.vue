<script setup lang="ts">
import { Search, Copy, Check, Download, Package, Layers, Clock, ArrowRight } from 'lucide-vue-next'
import type { PublicRegistryView } from '~/types/registry'
import { formatRelative, copyText, encodePathSegments } from '~/utils/format'

const props = defineProps<{
  registry: PublicRegistryView
}>()

interface PubPackageItem {
  id: string
  name: string
  origin: 'hosted' | 'cached'
  description?: string
  homepage?: string
  repository?: string
  latest_version?: string
  version_count: number
  is_discontinued: boolean
  downloads: number
  updated_at: string
}

interface PubListResponse {
  packages: PubPackageItem[]
  total: number
  page: number
  limit: number
}

const searchQuery = ref('')
const originFilter = ref<'' | 'hosted' | 'cached'>('')
const page = ref(1)
const limit = 15
const loading = ref(true)
const errorMsg = ref('')
const packages = ref<PubPackageItem[]>([])
const total = ref(0)
const copiedUrl = ref(false)

const hostedUrl = computed(() => {
  if (typeof window === 'undefined') return props.registry.base_path
  return `${window.location.origin}${props.registry.base_path}`
})

async function loadPackages() {
  loading.value = true
  errorMsg.value = ''
  try {
    const params = new URLSearchParams({
      page: String(page.value),
      limit: String(limit),
    })
    if (searchQuery.value.trim()) params.set('q', searchQuery.value.trim())
    if (originFilter.value) params.set('origin', originFilter.value)

    const res = await apiFetch<PubListResponse>(
      `${props.registry.base_path}/api/packages?${params.toString()}`,
    )
    packages.value = res.packages || []
    total.value = res.total || 0
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to load Pub packages'
  } finally {
    loading.value = false
  }
}

function handleSearch() {
  page.value = 1
  loadPackages()
}

function setOrigin(origin: '' | 'hosted' | 'cached') {
  originFilter.value = origin
  page.value = 1
  loadPackages()
}

async function copyHostedUrl() {
  if (await copyText(`export PUB_HOSTED_URL="${hostedUrl.value}"`)) {
    copiedUrl.value = true
    setTimeout(() => (copiedUrl.value = false), 2000)
  }
}

onMounted(loadPackages)
watch(() => props.registry.slug, () => {
  page.value = 1
  loadPackages()
})
</script>

<template>
  <div>
    <!-- pub.dev-inspired hero banner -->
    <section class="bg-[#122030] text-white border-b border-slate-800">
      <div class="mx-auto max-w-6xl px-4 sm:px-6 py-10 sm:py-12">
        <div class="flex flex-wrap items-center gap-2 text-xs text-sky-300 mb-3">
          <span class="inline-flex items-center gap-1 rounded-full bg-sky-500/15 border border-sky-400/25 px-2.5 py-0.5 font-medium">
            Dart &amp; Flutter Pub
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
              {{ registry.description || 'Self-hosted Dart and Flutter package repository.' }}
            </p>
          </div>

          <button
            type="button"
            class="inline-flex items-center gap-2.5 rounded-lg bg-slate-900/90 border border-slate-700 px-3.5 py-2 text-xs font-mono text-sky-200 hover:border-sky-400/60 transition-colors text-left"
            @click="copyHostedUrl"
          >
            <span class="truncate max-w-[300px] sm:max-w-[380px]">PUB_HOSTED_URL={{ hostedUrl }}</span>
            <Check v-if="copiedUrl" class="h-3.5 w-3.5 text-emerald-400 shrink-0" />
            <Copy v-else class="h-3.5 w-3.5 text-slate-400 shrink-0" />
          </button>
        </div>

        <!-- Centered pub.dev-style search input -->
        <form class="mt-7 max-w-2xl" @submit.prevent="handleSearch">
          <div class="relative flex items-center">
            <Search class="absolute left-3.5 h-4 w-4 text-slate-400 pointer-events-none" />
            <input
              v-model="searchQuery"
              type="text"
              placeholder="Search Dart & Flutter packages..."
              class="w-full h-11 rounded-full bg-white/10 border border-white/15 pl-10 pr-24 text-sm text-white placeholder:text-slate-400 focus:outline-none focus:ring-2 focus:ring-sky-400 focus:bg-white/15 transition-all"
            />
            <button
              type="submit"
              class="absolute right-1.5 h-8 rounded-full bg-[#0175c2] hover:bg-[#0284d9] px-4 text-xs font-medium text-white transition-colors"
            >
              Search
            </button>
          </div>
        </form>
      </div>
    </section>

    <!-- Package catalog -->
    <section class="mx-auto max-w-6xl px-4 sm:px-6 py-8">
      <div class="flex flex-wrap items-center justify-between gap-4 mb-6">
        <div class="flex items-center gap-1.5">
          <button
            type="button"
            :class="[
              'rounded-full px-3.5 py-1.5 text-xs font-medium transition-colors',
              originFilter === ''
                ? 'bg-[#0175c2] text-white'
                : 'bg-slate-100 text-slate-700 hover:bg-slate-200',
            ]"
            @click="setOrigin('')"
          >
            All Packages ({{ total }})
          </button>
          <button
            v-if="registry.mode !== 'proxy'"
            type="button"
            :class="[
              'rounded-full px-3.5 py-1.5 text-xs font-medium transition-colors',
              originFilter === 'hosted'
                ? 'bg-[#0175c2] text-white'
                : 'bg-slate-100 text-slate-700 hover:bg-slate-200',
            ]"
            @click="setOrigin('hosted')"
          >
            Hosted
          </button>
          <button
            v-if="registry.mode !== 'hosted'"
            type="button"
            :class="[
              'rounded-full px-3.5 py-1.5 text-xs font-medium transition-colors',
              originFilter === 'cached'
                ? 'bg-[#0175c2] text-white'
                : 'bg-slate-100 text-slate-700 hover:bg-slate-200',
            ]"
            @click="setOrigin('cached')"
          >
            Cached Upstream
          </button>
        </div>

        <p class="text-xs text-muted-foreground">
          Showing {{ packages.length }} of {{ total }} packages in <span class="font-mono">/r/{{ registry.slug }}</span>
        </p>
      </div>

      <SharedErrorBanner :message="errorMsg" class="mb-6" />

      <div v-if="loading" class="space-y-3">
        <div v-for="n in 4" :key="n" class="h-28 rounded-lg border bg-card animate-pulse" />
      </div>

      <SharedEmptyState
        v-else-if="packages.length === 0"
        title="No Pub packages indexed yet"
        description="Publish a package with `dart pub publish` or run `dart pub get` with PUB_HOSTED_URL pointed to this registry to populate cached packages."
      />

      <div v-else class="space-y-3">
        <NuxtLink
          v-for="pkg in packages"
          :key="pkg.id"
          :to="`/explore/${registry.slug}/packages/${encodePathSegments(pkg.name)}`"
          class="group block rounded-lg border bg-card p-5 hover:border-[#0175c2]/50 hover:shadow-md transition-all"
        >
          <div class="flex flex-col sm:flex-row sm:items-start justify-between gap-3">
            <div class="space-y-1.5 min-w-0">
              <div class="flex flex-wrap items-center gap-2">
                <h2 class="text-lg font-semibold text-[#0175c2] group-hover:underline">
                  {{ pkg.name }}
                </h2>
                <span
                  v-if="pkg.latest_version"
                  class="rounded bg-slate-100 px-2 py-0.5 font-mono text-xs font-medium text-slate-700"
                >
                  {{ pkg.latest_version }}
                </span>
                <UiBadge :variant="pkg.origin === 'hosted' ? 'info' : 'secondary'">
                  {{ pkg.origin === 'hosted' ? 'Hosted' : 'Cached' }}
                </UiBadge>
                <UiBadge v-if="pkg.is_discontinued" variant="warning">Discontinued</UiBadge>
              </div>
              <p class="text-sm text-slate-600 line-clamp-2">
                {{ pkg.description || 'No package description provided in pubspec.yaml.' }}
              </p>
              <div class="flex flex-wrap items-center gap-4 pt-1 text-xs text-muted-foreground">
                <span class="inline-flex items-center gap-1">
                  <Layers class="h-3.5 w-3.5" />
                  {{ pkg.version_count }} {{ pkg.version_count === 1 ? 'version' : 'versions' }}
                </span>
                <span class="inline-flex items-center gap-1">
                  <Download class="h-3.5 w-3.5" />
                  {{ pkg.downloads }} downloads
                </span>
                <span class="inline-flex items-center gap-1">
                  <Clock class="h-3.5 w-3.5" />
                  Updated {{ formatRelative(pkg.updated_at) }}
                </span>
              </div>
            </div>

            <div class="flex items-center gap-1 text-xs font-medium text-[#0175c2] shrink-0 self-end sm:self-center">
              <span>Inspect</span>
              <ArrowRight class="h-3.5 w-3.5 transition-transform group-hover:translate-x-0.5" />
            </div>
          </div>
        </NuxtLink>
      </div>

      <SharedPaginationBar
        :page="page"
        :limit="limit"
        :total="total"
        @change="(p) => { page = p; loadPackages() }"
      />
    </section>
  </div>
</template>
