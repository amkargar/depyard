<script setup lang="ts">
import { marked } from 'marked'
import {
  ArrowLeft,
  Copy,
  Check,
  Download,
  ExternalLink,
  AlertTriangle,
  ShieldCheck,
  Terminal,
  FileText,
  History,
  Layers,
  Code2,
} from 'lucide-vue-next'
import type { PublicRegistryView } from '~/types/registry'
import { formatBytes, formatDate, copyText } from '~/utils/format'

const props = defineProps<{
  registry: PublicRegistryView
  packageName: string
}>()

interface PubDetailVersion {
  version: string
  origin: string
  pubspec: Record<string, any>
  archive_url: string
  archive_sha256?: string
  archive_size: number
  archive_cached: boolean
  retracted: boolean
  published_at: string
}

interface PubDetailResponse {
  package: {
    name: string
    origin: string
    description?: string
    homepage?: string
    repository?: string
    documentation?: string
    readme?: string
    changelog?: string
    latest_version?: string
    version_count: number
    total_size_bytes: number
    is_discontinued: boolean
    replaced_by?: string
    uploaders?: string[]
    downloads: number
    updated_at: string
  }
  latest_pubspec?: Record<string, any>
  versions: PubDetailVersion[]
  base_url: string
  registry_slug: string
  registry_mode: string
}

type TabKey = 'readme' | 'changelog' | 'installing' | 'versions' | 'pubspec'

const activeTab = ref<TabKey>('readme')
const loading = ref(true)
const errorMsg = ref('')
const detail = ref<PubDetailResponse | null>(null)
const copiedKey = ref('')

const hostedUrl = computed(() => {
  if (typeof window === 'undefined') return props.registry.base_path
  return `${window.location.origin}${props.registry.base_path}`
})

const readmeHtml = computed(() => {
  const md = detail.value?.package?.readme
  if (!md) return ''
  return marked.parse(md, { async: false }) as string
})

const changelogHtml = computed(() => {
  const md = detail.value?.package?.changelog
  if (!md) return ''
  return marked.parse(md, { async: false }) as string
})

const sdkConstraint = computed(() => {
  const env = detail.value?.latest_pubspec?.environment
  return env && typeof env === 'object' ? env : null
})

const dependencies = computed(() => {
  const deps = detail.value?.latest_pubspec?.dependencies
  if (!deps || typeof deps !== 'object') return []
  return Object.entries(deps as Record<string, any>)
})

async function loadDetail() {
  loading.value = true
  errorMsg.value = ''
  try {
    detail.value = await apiFetch<PubDetailResponse>(
      `${props.registry.base_path}/api/packages/${encodeURIComponent(props.packageName)}/detail`,
    )
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to load Pub package details'
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
        class="inline-flex items-center gap-1.5 text-xs font-medium text-muted-foreground hover:text-foreground transition-colors"
      >
        <ArrowLeft class="h-3.5 w-3.5" />
        Back to {{ registry.name }}
      </NuxtLink>
    </div>

    <SharedErrorBanner :message="errorMsg" class="mb-6" />

    <div v-if="loading" class="space-y-4">
      <div class="h-36 rounded-xl border bg-card animate-pulse" />
      <div class="h-72 rounded-xl border bg-card animate-pulse" />
    </div>

    <template v-else-if="detail">
      <!-- Discontinued banner -->
      <div
        v-if="detail.package.is_discontinued"
        class="mb-5 flex items-start gap-3 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900"
      >
        <AlertTriangle class="h-4 w-4 shrink-0 mt-0.5 text-amber-600" />
        <div>
          <strong>Discontinued package.</strong>
          <span v-if="detail.package.replaced_by">
            Consider migrating to
            <NuxtLink
              :to="`/explore/${registry.slug}/packages/${encodeURIComponent(detail.package.replaced_by)}`"
              class="underline font-medium"
            >
              {{ detail.package.replaced_by }}
            </NuxtLink>.
          </span>
        </div>
      </div>

      <!-- pub.dev-style package header -->
      <UiCard class="p-6 mb-6">
        <div class="flex flex-col md:flex-row md:items-start justify-between gap-4">
          <div class="space-y-2">
            <div class="flex flex-wrap items-center gap-2.5">
              <h1 class="text-2xl sm:text-3xl font-bold tracking-tight text-slate-900">
                {{ detail.package.name }}
              </h1>
              <span class="rounded-md bg-[#0175c2]/10 text-[#0175c2] px-2.5 py-0.5 font-mono text-sm font-semibold">
                {{ detail.package.latest_version || '0.0.0' }}
              </span>
              <UiBadge :variant="detail.package.origin === 'hosted' ? 'info' : 'secondary'">
                {{ detail.package.origin === 'hosted' ? 'Hosted Locally' : 'Cached Upstream' }}
              </UiBadge>
            </div>
            <p class="text-sm text-slate-600 max-w-2xl">
              {{ detail.package.description || 'No description provided.' }}
            </p>
            <div class="flex flex-wrap items-center gap-3 text-xs text-muted-foreground pt-1">
              <span>Registry: <strong class="font-mono text-foreground">/r/{{ registry.slug }}</strong></span>
              <span>•</span>
              <span>Updated {{ formatDate(detail.package.updated_at) }}</span>
              <template v-if="sdkConstraint?.sdk">
                <span>•</span>
                <span class="rounded bg-slate-100 px-2 py-0.5 font-mono text-[11px] text-slate-700">
                  Dart SDK {{ sdkConstraint.sdk }}
                </span>
              </template>
            </div>
          </div>

          <div class="flex flex-col gap-2 shrink-0">
            <button
              type="button"
              class="inline-flex items-center justify-between gap-3 rounded-lg border bg-slate-900 text-slate-100 px-3.5 py-2 text-xs font-mono hover:bg-slate-800 transition-colors"
              title="Copy the Dart install command with PUB_HOSTED_URL preset"
              @click="copySnippet('add', `PUB_HOSTED_URL=${hostedUrl} dart pub add ${detail.package.name}`)"
            >
              <span>dart pub add {{ detail.package.name }}</span>
              <Check v-if="copiedKey === 'add'" class="h-3.5 w-3.5 text-emerald-400" />
              <Copy v-else class="h-3.5 w-3.5 text-slate-400" />
            </button>
            <button
              type="button"
              class="inline-flex items-center justify-between gap-3 rounded-lg border bg-slate-900 text-slate-100 px-3.5 py-2 text-xs font-mono hover:bg-slate-800 transition-colors"
              title="Copy the Flutter install command with PUB_HOSTED_URL preset"
              @click="copySnippet('add-flutter', `PUB_HOSTED_URL=${hostedUrl} flutter pub add ${detail.package.name}`)"
            >
              <span>flutter pub add {{ detail.package.name }}</span>
              <Check v-if="copiedKey === 'add-flutter'" class="h-3.5 w-3.5 text-emerald-400" />
              <Copy v-else class="h-3.5 w-3.5 text-slate-400" />
            </button>
          </div>
        </div>

        <!-- pub.dev horizontal tabs -->
        <div class="mt-6 -mb-6 flex items-center gap-1 border-t pt-1 overflow-x-auto">
          <button
            v-for="tab in [
              { key: 'readme', label: 'Readme', icon: FileText },
              { key: 'changelog', label: 'Changelog', icon: History },
              { key: 'installing', label: 'Installing', icon: Terminal },
              { key: 'versions', label: `Versions (${detail.versions.length})`, icon: Layers },
              { key: 'pubspec', label: 'Pubspec', icon: Code2 },
            ]"
            :key="tab.key"
            type="button"
            :class="[
              'inline-flex items-center gap-1.5 border-b-2 px-4 py-3 text-sm font-medium whitespace-nowrap transition-colors',
              activeTab === tab.key
                ? 'border-[#0175c2] text-[#0175c2]'
                : 'border-transparent text-muted-foreground hover:text-foreground',
            ]"
            @click="activeTab = tab.key as TabKey"
          >
            <component :is="tab.icon" class="h-3.5 w-3.5" />
            {{ tab.label }}
          </button>
        </div>
      </UiCard>

      <!-- Main content + Sidebar -->
      <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
        <div class="lg:col-span-2 space-y-6">
          <!-- README tab -->
          <UiCard v-if="activeTab === 'readme'" class="p-6">
            <div v-if="readmeHtml" class="pub-markdown" v-html="readmeHtml" />
            <div v-else class="py-10 text-center text-sm text-muted-foreground">
              No README.md was found in this package archive.
            </div>
          </UiCard>

          <!-- CHANGELOG tab -->
          <UiCard v-if="activeTab === 'changelog'" class="p-6">
            <div v-if="changelogHtml" class="pub-markdown" v-html="changelogHtml" />
            <div v-else class="py-10 text-center text-sm text-muted-foreground">
              No CHANGELOG.md was found in this package archive.
            </div>
          </UiCard>

          <!-- INSTALLING tab -->
          <UiCard v-if="activeTab === 'installing'" class="p-6 space-y-5">
            <div>
              <h2 class="text-base font-semibold text-foreground">1. Configure PUB_HOSTED_URL</h2>
              <p class="text-xs text-muted-foreground mt-1">
                Point Dart or Flutter to this registry instance:
              </p>
              <div class="mt-2 relative rounded-lg bg-slate-900 p-3.5 font-mono text-xs text-slate-100">
                <code>export PUB_HOSTED_URL="{{ hostedUrl }}"</code>
                <button
                  type="button"
                  class="absolute right-2.5 top-2.5 rounded p-1 text-slate-400 hover:text-white"
                  @click="copySnippet('env', `export PUB_HOSTED_URL=&quot;${hostedUrl}&quot;`)"
                >
                  <Check v-if="copiedKey === 'env'" class="h-3.5 w-3.5 text-emerald-400" />
                  <Copy v-else class="h-3.5 w-3.5" />
                </button>
              </div>
            </div>

            <div v-if="registry.visibility === 'private'">
              <h2 class="text-base font-semibold text-foreground">2. Authenticate with a Scoped Token</h2>
              <p class="text-xs text-muted-foreground mt-1">
                Create a token scoped to <code class="font-mono">{{ registry.slug }}</code> in the Admin Console, then register it with Dart:
              </p>
              <pre class="mt-2 rounded-lg bg-slate-900 p-3.5 font-mono text-xs text-slate-100 overflow-x-auto">dart pub token add {{ hostedUrl }}</pre>
            </div>

            <div>
              <h2 class="text-base font-semibold text-foreground">
                {{ registry.visibility === 'private' ? '3.' : '2.' }} Add the dependency
              </h2>
              <p class="text-xs text-muted-foreground mt-1">
                Works for both toolchains — pick the command matching your project:
              </p>
              <div class="mt-2 grid grid-cols-1 sm:grid-cols-2 gap-2">
                <div class="relative rounded-lg bg-slate-900 p-3.5 pr-10 font-mono text-xs text-slate-100">
                  <code>dart pub add {{ detail.package.name }}</code>
                  <button
                    type="button"
                    class="absolute right-2.5 top-2.5 rounded p-1 text-slate-400 hover:text-white"
                    :aria-label="`Copy dart pub add ${detail.package.name}`"
                    @click="copySnippet('install-dart', `PUB_HOSTED_URL=${hostedUrl} dart pub add ${detail.package.name}`)"
                  >
                    <Check v-if="copiedKey === 'install-dart'" class="h-3.5 w-3.5 text-emerald-400" />
                    <Copy v-else class="h-3.5 w-3.5" />
                  </button>
                </div>
                <div class="relative rounded-lg bg-slate-900 p-3.5 pr-10 font-mono text-xs text-slate-100">
                  <code>flutter pub add {{ detail.package.name }}</code>
                  <button
                    type="button"
                    class="absolute right-2.5 top-2.5 rounded p-1 text-slate-400 hover:text-white"
                    :aria-label="`Copy flutter pub add ${detail.package.name}`"
                    @click="copySnippet('install-flutter', `PUB_HOSTED_URL=${hostedUrl} flutter pub add ${detail.package.name}`)"
                  >
                    <Check v-if="copiedKey === 'install-flutter'" class="h-3.5 w-3.5 text-emerald-400" />
                    <Copy v-else class="h-3.5 w-3.5" />
                  </button>
                </div>
              </div>
            </div>

            <div>
              <h2 class="text-base font-semibold text-foreground">
                {{ registry.visibility === 'private' ? '4.' : '3.' }} Or pin it in pubspec.yaml
              </h2>
              <p class="text-xs text-muted-foreground mt-1">
                Explicitly pin this hosted repository in your project's <code class="font-mono">pubspec.yaml</code>:
              </p>
              <pre class="mt-2 rounded-lg bg-slate-900 p-3.5 font-mono text-xs text-slate-100 overflow-x-auto">dependencies:
  {{ detail.package.name }}:
    hosted: {{ hostedUrl }}
    version: ^{{ detail.package.latest_version || '1.0.0' }}</pre>
            </div>
          </UiCard>

          <!-- VERSIONS tab -->
          <UiCard v-if="activeTab === 'versions'" class="overflow-hidden">
            <div class="overflow-x-auto">
              <table class="w-full text-sm">
                <thead>
                  <tr class="border-b bg-muted/40 text-left text-xs font-medium text-muted-foreground">
                    <th class="px-4 py-3">Version</th>
                    <th class="px-4 py-3">Published</th>
                    <th class="px-4 py-3">Size</th>
                    <th class="px-4 py-3">SHA-256</th>
                    <th class="px-4 py-3 text-right">Archive</th>
                  </tr>
                </thead>
                <tbody class="divide-y">
                  <tr v-for="v in detail.versions" :key="v.version" class="hover:bg-muted/20">
                    <td class="px-4 py-3">
                      <div class="flex items-center gap-2">
                        <span class="font-mono font-medium text-foreground">{{ v.version }}</span>
                        <UiBadge v-if="v.retracted" variant="warning">Retracted</UiBadge>
                        <UiBadge v-if="v.version === detail.package.latest_version" variant="info">Latest</UiBadge>
                      </div>
                    </td>
                    <td class="px-4 py-3 text-xs text-muted-foreground">{{ formatDate(v.published_at) }}</td>
                    <td class="px-4 py-3 text-xs text-muted-foreground">
                      {{ v.archive_size > 0 ? formatBytes(v.archive_size) : 'On-demand' }}
                    </td>
                    <td class="px-4 py-3">
                      <button
                        v-if="v.archive_sha256"
                        type="button"
                        class="inline-flex items-center gap-1 font-mono text-xs text-muted-foreground hover:text-foreground"
                        @click="copySnippet('sha-' + v.version, v.archive_sha256)"
                      >
                        <span>{{ v.archive_sha256.slice(0, 12) }}…</span>
                        <Check v-if="copiedKey === 'sha-' + v.version" class="h-3 w-3 text-emerald-600" />
                        <Copy v-else class="h-3 w-3" />
                      </button>
                      <span v-else class="text-xs text-muted-foreground">—</span>
                    </td>
                    <td class="px-4 py-3 text-right">
                      <a
                        :href="`${registry.base_path}/packages/${encodeURIComponent(detail.package.name)}/versions/${encodeURIComponent(v.version)}.tar.gz`"
                        class="inline-flex items-center gap-1 rounded-md border px-2.5 py-1 text-xs font-medium hover:bg-muted transition-colors"
                      >
                        <Download class="h-3 w-3" />
                        .tar.gz
                      </a>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </UiCard>

          <!-- PUBSPEC tab -->
          <UiCard v-if="activeTab === 'pubspec'" class="p-6">
            <h2 class="text-sm font-semibold mb-3">Latest pubspec.yaml (parsed)</h2>
            <pre class="rounded-lg bg-slate-900 p-4 text-xs font-mono text-slate-100 overflow-x-auto">{{ JSON.stringify(detail.latest_pubspec || {}, null, 2) }}</pre>
          </UiCard>
        </div>

        <!-- pub.dev-style right metadata sidebar -->
        <aside class="space-y-4">
          <UiCard class="p-5 space-y-4">
            <h3 class="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Package Metrics</h3>
            <div class="grid grid-cols-2 gap-3">
              <div class="rounded-lg bg-muted/40 p-3">
                <div class="text-lg font-bold text-foreground">{{ detail.package.downloads }}</div>
                <div class="text-[11px] text-muted-foreground">Downloads</div>
              </div>
              <div class="rounded-lg bg-muted/40 p-3">
                <div class="text-lg font-bold text-foreground">{{ detail.package.version_count }}</div>
                <div class="text-[11px] text-muted-foreground">Versions</div>
              </div>
            </div>

            <div class="border-t pt-4 space-y-2.5 text-xs">
              <div class="flex items-center justify-between">
                <span class="text-muted-foreground">Stored Size</span>
                <span class="font-medium">{{ formatBytes(detail.package.total_size_bytes) }}</span>
              </div>
              <div class="flex items-center justify-between">
                <span class="text-muted-foreground">Origin</span>
                <span class="font-medium capitalize">{{ detail.package.origin }}</span>
              </div>
              <div class="flex items-center justify-between">
                <span class="text-muted-foreground">Registry Mode</span>
                <span class="font-medium capitalize">{{ detail.registry_mode }}</span>
              </div>
            </div>

            <div v-if="detail.package.repository || detail.package.homepage || detail.package.documentation" class="border-t pt-4 space-y-2">
              <h4 class="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Links</h4>
              <a
                v-if="detail.package.repository"
                :href="detail.package.repository"
                target="_blank"
                rel="noopener noreferrer"
                class="flex items-center gap-1.5 text-xs text-[#0175c2] hover:underline break-all"
              >
                <ExternalLink class="h-3.5 w-3.5 shrink-0" />
                Repository
              </a>
              <a
                v-if="detail.package.homepage"
                :href="detail.package.homepage"
                target="_blank"
                rel="noopener noreferrer"
                class="flex items-center gap-1.5 text-xs text-[#0175c2] hover:underline break-all"
              >
                <ExternalLink class="h-3.5 w-3.5 shrink-0" />
                Homepage
              </a>
              <a
                v-if="detail.package.documentation"
                :href="detail.package.documentation"
                target="_blank"
                rel="noopener noreferrer"
                class="flex items-center gap-1.5 text-xs text-[#0175c2] hover:underline break-all"
              >
                <ExternalLink class="h-3.5 w-3.5 shrink-0" />
                Documentation
              </a>
            </div>

            <div v-if="detail.package.uploaders?.length" class="border-t pt-4 space-y-2">
              <h4 class="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Uploaders</h4>
              <div class="flex flex-col gap-1.5">
                <span
                  v-for="u in detail.package.uploaders"
                  :key="u"
                  class="inline-flex items-center gap-1.5 text-xs text-foreground"
                >
                  <ShieldCheck class="h-3.5 w-3.5 text-emerald-600" />
                  {{ u }}
                </span>
              </div>
            </div>

            <div v-if="dependencies.length" class="border-t pt-4 space-y-2">
              <h4 class="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Dependencies</h4>
              <div class="flex flex-wrap gap-1.5">
                <NuxtLink
                  v-for="[depName] in dependencies"
                  :key="depName"
                  :to="`/explore/${registry.slug}/packages/${encodeURIComponent(depName)}`"
                  class="rounded bg-slate-100 hover:bg-slate-200 px-2 py-0.5 text-xs font-mono text-[#0175c2] transition-colors"
                >
                  {{ depName }}
                </NuxtLink>
              </div>
            </div>
          </UiCard>
        </aside>
      </div>
    </template>
  </div>
</template>
