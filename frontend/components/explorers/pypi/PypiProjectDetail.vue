<script setup lang="ts">
import { Copy, Check, Terminal, Package, Layers, Clock, Download, FileCode, ExternalLink, AlertTriangle } from 'lucide-vue-next'
import type { PublicRegistryView } from '~/types/registry'
import { formatDate, formatBytes, copyText } from '~/utils/format'

const props = defineProps<{
  registry: PublicRegistryView
  packageName: string
}>()

interface PyFileItem {
  filename: string
  version: string
  packagetype: string
  python_version?: string
  requires_python?: string
  digest?: string
  size_bytes: number
  yanked: boolean
  yanked_reason?: string
  origin: 'hosted' | 'cached'
  upload_time?: string
  has_metadata: boolean
  url: string
}

interface PyProjectDetail {
  project: {
    id: string
    name: string
    display_name?: string
    origin: 'hosted' | 'cached'
    summary?: string
    description?: string
    homepage?: string
    author?: string
    author_email?: string
    license?: string
    requires_python?: string
    classifiers?: string[]
    project_urls?: Record<string, string>
    latest_version?: string
    release_count: number
    file_count: number
    total_size_bytes: number
    downloads: number
    updated_at?: string
  }
  releases: PyFileItem[]
  versions: string[]
  install_url: string
  simple_url: string
  requires_python?: string
  summary?: string
  project_urls?: Record<string, string>
}

const loading = ref(true)
const errorMsg = ref('')
const detail = ref<PyProjectDetail | null>(null)
const copiedKey = ref('')

const hostedUrl = computed(() => {
  if (typeof window === 'undefined') return props.registry.base_path
  return `${window.location.origin}${props.registry.base_path}`
})

const indexUrl = computed(() => `${hostedUrl.value}/simple`)

/** Group the flat file list by version, newest first. */
const releasesByVersion = computed(() => {
  const groups = new Map<string, PyFileItem[]>()
  for (const file of detail.value?.releases || []) {
    const list = groups.get(file.version) || []
    list.push(file)
    groups.set(file.version, list)
  }
  return Array.from(groups.entries()).map(([version, files]) => ({
    version,
    files,
  }))
})

async function loadDetail() {
  loading.value = true
  errorMsg.value = ''
  try {
    detail.value = await apiFetch<PyProjectDetail>(
      `${props.registry.base_path}/api/projects/${encodeURIComponent(props.packageName)}`,
    )
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to load Python package details'
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

function pipInstallCommand(project: string) {
  return `pip install --index-url ${indexUrl.value}/ ${project}`
}

function twineUploadCommand(project: string) {
  return `twine upload --repository-url ${hostedUrl.value}/legacy/ dist/*`
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
      <!-- Project header -->
      <UiCard class="p-6">
        <div class="flex flex-col md:flex-row md:items-start justify-between gap-4">
          <div class="space-y-2 min-w-0">
            <div class="flex flex-wrap items-center gap-2.5">
              <h1 class="text-2xl sm:text-3xl font-bold tracking-tight text-slate-900 font-mono">
                {{ detail.project.display_name || detail.project.name }}
              </h1>
              <span
                v-if="detail.project.latest_version"
                class="rounded-md bg-yellow-500/15 text-yellow-700 px-2.5 py-0.5 font-mono text-sm font-semibold"
              >
                {{ detail.project.latest_version }}
              </span>
              <UiBadge :variant="detail.project.origin === 'hosted' ? 'info' : 'secondary'">
                {{ detail.project.origin === 'hosted' ? 'Hosted Locally' : 'Cached Upstream' }}
              </UiBadge>
            </div>
            <p v-if="detail.project.summary" class="text-sm text-slate-600 max-w-2xl">
              {{ detail.project.summary }}
            </p>
            <div class="flex flex-wrap items-center gap-3 text-xs text-muted-foreground pt-1">
              <span v-if="detail.project.license">License: <strong class="text-foreground">{{ detail.project.license }}</strong></span>
              <span v-if="detail.project.author">•</span>
              <span v-if="detail.project.author">{{ detail.project.author }}</span>
              <span v-if="detail.project.requires_python">•</span>
              <span v-if="detail.project.requires_python" class="font-mono">
                Python {{ detail.project.requires_python }}
              </span>
              <span>•</span>
              <span>Updated {{ formatDate(detail.project.updated_at) }}</span>
            </div>
          </div>

          <div class="flex flex-col gap-2 shrink-0">
            <button
              type="button"
              class="inline-flex items-center justify-between gap-3 rounded-lg border bg-slate-900 text-slate-100 px-3.5 py-2 text-xs font-mono hover:bg-slate-800 transition-colors"
              title="Copy the package index URL"
              @click="copySnippet('index', indexUrl)"
            >
              <Terminal class="h-4 w-4 shrink-0 text-yellow-300" />
              <span class="truncate">{{ indexUrl }}</span>
              <Check v-if="copiedKey === 'index'" class="h-4 w-4 shrink-0 text-emerald-400" />
              <Copy v-else class="h-4 w-4 shrink-0 text-slate-400" />
            </button>
          </div>
        </div>

        <!-- Install / publish snippets -->
        <div class="mt-5 grid grid-cols-1 sm:grid-cols-2 gap-2">
          <div class="relative rounded-lg bg-slate-900 p-3.5 pr-10 font-mono text-xs text-slate-100">
            <code>{{ pipInstallCommand(detail.project.name) }}</code>
            <button
              type="button"
              class="absolute right-2.5 top-2.5 rounded p-1 text-slate-400 hover:text-white"
              aria-label="Copy pip install command"
              @click="copySnippet('pip', pipInstallCommand(detail.project.name))"
            >
              <Check v-if="copiedKey === 'pip'" class="h-3.5 w-3.5 text-emerald-400" />
              <Copy v-else class="h-3.5 w-3.5" />
            </button>
          </div>
          <div class="relative rounded-lg bg-slate-900 p-3.5 pr-10 font-mono text-xs text-slate-100">
            <code>{{ twineUploadCommand(detail.project.name) }}</code>
            <button
              type="button"
              class="absolute right-2.5 top-2.5 rounded p-1 text-slate-400 hover:text-white"
              aria-label="Copy twine upload command"
              @click="copySnippet('twine', twineUploadCommand(detail.project.name))"
            >
              <Check v-if="copiedKey === 'twine'" class="h-3.5 w-3.5 text-emerald-400" />
              <Copy v-else class="h-3.5 w-3.5" />
            </button>
          </div>
        </div>

        <!-- Project links -->
        <div v-if="detail.project.homepage || Object.keys(detail.project.project_urls || {}).length" class="mt-4 flex flex-wrap gap-2">
          <a
            v-if="detail.project.homepage"
            :href="detail.project.homepage"
            target="_blank"
            rel="noopener noreferrer"
            class="inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
          >
            <ExternalLink class="h-3 w-3" />
            Homepage
          </a>
          <a
            v-for="(url, label) in detail.project.project_urls || {}"
            :key="label"
            :href="url"
            target="_blank"
            rel="noopener noreferrer"
            class="inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
          >
            <ExternalLink class="h-3 w-3" />
            {{ label }}
          </a>
        </div>
      </UiCard>

      <!-- Stats -->
      <div class="grid grid-cols-2 sm:grid-cols-4 gap-3">
        <UiCard class="p-4">
          <div class="flex items-center gap-2 text-xs text-muted-foreground">
            <Layers class="h-3.5 w-3.5" />
            Releases
          </div>
          <p class="mt-1.5 text-xl font-semibold">{{ detail.project.release_count }}</p>
        </UiCard>
        <UiCard class="p-4">
          <div class="flex items-center gap-2 text-xs text-muted-foreground">
            <FileCode class="h-3.5 w-3.5" />
            Files
          </div>
          <p class="mt-1.5 text-xl font-semibold">{{ detail.project.file_count }}</p>
        </UiCard>
        <UiCard class="p-4">
          <div class="flex items-center gap-2 text-xs text-muted-foreground">
            <Download class="h-3.5 w-3.5" />
            Downloads
          </div>
          <p class="mt-1.5 text-xl font-semibold">{{ detail.project.downloads }}</p>
        </UiCard>
        <UiCard class="p-4">
          <div class="flex items-center gap-2 text-xs text-muted-foreground">
            <Package class="h-3.5 w-3.5" />
            Size
          </div>
          <p class="mt-1.5 text-xl font-semibold">{{ formatBytes(detail.project.total_size_bytes) }}</p>
        </UiCard>
      </div>

      <!-- Releases -->
      <UiCard class="p-0 overflow-hidden">
        <div class="border-b px-5 py-3.5">
          <h2 class="text-sm font-semibold text-foreground">Distribution files</h2>
          <p class="mt-0.5 text-xs text-muted-foreground">
            Every wheel and source distribution published for this project.
          </p>
        </div>

        <div v-if="releasesByVersion.length === 0" class="p-5">
          <SharedEmptyState
            title="No distribution files"
            description="This project has no published wheels or source distributions yet."
          />
        </div>

        <div v-else class="divide-y">
          <div v-for="release in releasesByVersion" :key="release.version" class="p-5">
            <div class="mb-3 flex flex-wrap items-center gap-2">
              <h3 class="font-mono text-sm font-semibold text-foreground">{{ release.version }}</h3>
              <span class="text-xs text-muted-foreground">
                {{ release.files.length }} file{{ release.files.length === 1 ? '' : 's' }}
              </span>
            </div>

            <div class="space-y-2">
              <div
                v-for="file in release.files"
                :key="file.filename"
                class="rounded-lg border bg-muted/30 p-3"
              >
                <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-2">
                  <div class="min-w-0 flex items-center gap-2">
                    <FileCode class="h-4 w-4 shrink-0 text-yellow-600" />
                    <a
                      :href="file.url"
                      class="font-mono text-xs font-medium text-foreground hover:text-yellow-700 truncate transition-colors"
                    >
                      {{ file.filename }}
                    </a>
                    <span
                      v-if="file.yanked"
                      class="inline-flex items-center gap-1 rounded bg-red-500/10 px-1.5 py-0.5 text-[10px] font-medium text-red-700"
                      :title="file.yanked_reason || 'This release has been yanked'"
                    >
                      <AlertTriangle class="h-3 w-3" />
                      yanked
                    </span>
                  </div>
                  <div class="flex flex-wrap items-center gap-3 text-xs text-muted-foreground shrink-0">
                    <span v-if="file.python_version" class="font-mono">{{ file.python_version }}</span>
                    <span>{{ formatBytes(file.size_bytes) }}</span>
                    <span v-if="file.upload_time" class="inline-flex items-center gap-1">
                      <Clock class="h-3 w-3" />
                      {{ formatDate(file.upload_time) }}
                    </span>
                  </div>
                </div>
                <div v-if="file.digest" class="mt-1.5 truncate font-mono text-[10px] text-muted-foreground/80">
                  sha256:{{ file.digest }}
                </div>
              </div>
            </div>
          </div>
        </div>
      </UiCard>
    </div>
  </div>
</template>
