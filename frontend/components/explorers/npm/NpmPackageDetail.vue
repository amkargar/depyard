<script setup lang="ts">
import { Copy, Check, Terminal, Package, Layers, Clock, Download, FileArchive, ArrowLeft } from 'lucide-vue-next'
import type { PublicRegistryView } from '~/types/registry'
import { formatBytes, formatDate, copyText } from '~/utils/format'

const props = defineProps<{
  registry: PublicRegistryView
  packageName: string
}>()

interface NpmVersionItem {
  version: string
  filename: string
  digest?: string
  shasum?: string
  integrity?: string
  size_bytes: number
  origin: 'hosted' | 'cached' | 'mixed'
  published_at?: string
  url: string
}

interface NpmPackageDetail {
  package: {
    id: string
    name: string
    description?: string
    origin: 'hosted' | 'cached' | 'mixed'
    latest_version?: string
    version_count: number
    total_size_bytes: number
    downloads: number
    updated_at?: string
  }
  versions: NpmVersionItem[]
  registry_url: string
  install_url: string
  publish_url: string
}

const loading = ref(true)
const errorMsg = ref('')
const detail = ref<NpmPackageDetail | null>(null)
const copiedKey = ref('')

async function loadDetail() {
  loading.value = true
  errorMsg.value = ''
  try {
    detail.value = await apiFetch<NpmPackageDetail>(
      `${props.registry.base_path}/api/packages/${encodeURIComponent(props.packageName)}`,
    )
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to load npm package'
    detail.value = null
  } finally {
    loading.value = false
  }
}

async function copy(key: string, text: string) {
  if (await copyText(text)) {
    copiedKey.value = key
    setTimeout(() => (copiedKey.value = ''), 2000)
  }
}

const installCommand = computed(() =>
  detail.value ? `npm install ${detail.value.package.name} --registry=${detail.value.registry_url}` : '',
)

onMounted(loadDetail)
watch(() => [props.registry.slug, props.packageName], loadDetail)
</script>

<template>
  <div>
    <div v-if="loading" class="mx-auto max-w-6xl px-4 sm:px-6 py-10">
      <div class="h-64 rounded-xl border bg-card animate-pulse" />
    </div>

    <div v-else-if="errorMsg" class="mx-auto max-w-3xl px-4 sm:px-6 py-12 space-y-4">
      <SharedErrorBanner :message="errorMsg" />
      <NuxtLink :to="`/explore/${registry.slug}`">
        <UiButton variant="outline"><ArrowLeft class="h-4 w-4 mr-2" />Back to Registry</UiButton>
      </NuxtLink>
    </div>

    <div v-else-if="detail" class="mx-auto max-w-6xl px-4 sm:px-6 py-8 space-y-5">
      <NuxtLink :to="`/explore/${registry.slug}`"
        class="inline-flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground">
        <ArrowLeft class="h-3.5 w-3.5" /> Back to {{ registry.name }}
      </NuxtLink>

      <UiCard class="p-5">
        <div class="flex flex-col lg:flex-row lg:items-start justify-between gap-5">
          <div class="min-w-0">
            <div class="flex flex-wrap items-center gap-3">
              <h1 class="font-mono text-xl sm:text-2xl font-semibold text-foreground truncate">
                {{ detail.package.name }}
              </h1>
              <span v-if="detail.package.latest_version"
                class="rounded-md bg-red-500/15 text-red-700 px-2.5 py-0.5 font-mono text-sm font-semibold">
                {{ detail.package.latest_version }}
              </span>
              <UiBadge :variant="detail.package.origin === 'hosted' ? 'info' : 'secondary'">
                {{ detail.package.origin === 'hosted' ? 'Hosted Locally' : detail.package.origin === 'mixed' ? 'Hosted + Cached' : 'Cached Upstream' }}
              </UiBadge>
            </div>
            <p v-if="detail.package.description" class="mt-2 text-sm text-muted-foreground max-w-2xl">
              {{ detail.package.description }}
            </p>
          </div>
          <button type="button"
            class="inline-flex items-center justify-between gap-3 rounded-lg border bg-slate-900 text-slate-100 px-3.5 py-2 text-xs font-mono hover:bg-slate-800 transition-colors"
            @click="copy('registry', detail.registry_url)">
            <Terminal class="h-4 w-4 shrink-0 text-red-300" />
            <span class="truncate">{{ detail.registry_url }}</span>
            <Check v-if="copiedKey === 'registry'" class="h-4 w-4 shrink-0 text-emerald-400" />
            <Copy v-else class="h-4 w-4 shrink-0 text-slate-400" />
          </button>
        </div>

        <div class="mt-5 grid grid-cols-1 sm:grid-cols-2 gap-2">
          <div class="relative rounded-lg bg-slate-900 p-3.5 pr-10 font-mono text-xs text-slate-100">
            <code>{{ installCommand }}</code>
            <button type="button" class="absolute right-2.5 top-2.5 rounded p-1 text-slate-400 hover:text-white"
              aria-label="Copy install command" @click="copy('install', installCommand)">
              <Check v-if="copiedKey === 'install'" class="h-3.5 w-3.5 text-emerald-400" />
              <Copy v-else class="h-3.5 w-3.5" />
            </button>
          </div>
          <div class="relative rounded-lg bg-slate-900 p-3.5 pr-10 font-mono text-xs text-slate-100">
            <code>npm publish --registry={{ detail.publish_url }}</code>
            <button type="button" class="absolute right-2.5 top-2.5 rounded p-1 text-slate-400 hover:text-white"
              aria-label="Copy publish command" @click="copy('publish', `npm publish --registry=${detail.publish_url}`)">
              <Check v-if="copiedKey === 'publish'" class="h-3.5 w-3.5 text-emerald-400" />
              <Copy v-else class="h-3.5 w-3.5" />
            </button>
          </div>
        </div>
      </UiCard>

      <div class="grid grid-cols-2 sm:grid-cols-4 gap-3">
        <UiCard class="p-4">
          <div class="flex items-center gap-2 text-xs text-muted-foreground"><Layers class="h-3.5 w-3.5" />Versions</div>
          <p class="mt-1.5 text-xl font-semibold">{{ detail.package.version_count }}</p>
        </UiCard>
        <UiCard class="p-4">
          <div class="flex items-center gap-2 text-xs text-muted-foreground"><Package class="h-3.5 w-3.5" />Tarballs</div>
          <p class="mt-1.5 text-xl font-semibold">{{ detail.versions.length }}</p>
        </UiCard>
        <UiCard class="p-4">
          <div class="flex items-center gap-2 text-xs text-muted-foreground"><Download class="h-3.5 w-3.5" />Downloads</div>
          <p class="mt-1.5 text-xl font-semibold">{{ detail.package.downloads }}</p>
        </UiCard>
        <UiCard class="p-4">
          <div class="flex items-center gap-2 text-xs text-muted-foreground"><FileArchive class="h-3.5 w-3.5" />Size</div>
          <p class="mt-1.5 text-xl font-semibold">{{ formatBytes(detail.package.total_size_bytes) }}</p>
        </UiCard>
      </div>

      <UiCard class="p-0 overflow-hidden">
        <div class="border-b px-5 py-3.5">
          <h2 class="text-sm font-semibold text-foreground">Published versions</h2>
          <p class="mt-0.5 text-xs text-muted-foreground">Tarballs are served from this registry; uncached files are fetched from upstream on demand.</p>
        </div>
        <div v-if="detail.versions.length === 0" class="p-5">
          <SharedEmptyState title="No versions" description="This package has no published versions yet." />
        </div>
        <div v-else class="divide-y">
          <div v-for="version in detail.versions" :key="version.version" class="p-4 sm:p-5">
            <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-2">
              <div class="flex items-center gap-2 min-w-0">
                <FileArchive class="h-4 w-4 shrink-0 text-red-600" />
                <a :href="version.url" class="font-mono text-xs font-medium text-foreground hover:text-red-700 truncate">
                  {{ version.filename }}
                </a>
                <span class="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
                  {{ version.version }}
                </span>
              </div>
              <div class="flex flex-wrap items-center gap-3 text-xs text-muted-foreground shrink-0">
                <span>{{ formatBytes(version.size_bytes) }}</span>
                <span v-if="version.published_at" class="inline-flex items-center gap-1"><Clock class="h-3 w-3" />{{ formatDate(version.published_at) }}</span>
                <UiBadge variant="outline">{{ version.origin }}</UiBadge>
              </div>
            </div>
            <div v-if="version.digest || version.integrity" class="mt-1.5 truncate font-mono text-[10px] text-muted-foreground/80">
              <span v-if="version.digest">sha256:{{ version.digest }}</span>
              <span v-else-if="version.integrity">{{ version.integrity }}</span>
            </div>
          </div>
        </div>
      </UiCard>
    </div>
  </div>
</template>
