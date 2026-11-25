<script setup lang="ts">
import { Plus, Trash2, RotateCcw, ServerCog, Globe2 } from 'lucide-vue-next'
import type { FlutterDownloadStorageSetting } from '~/types/registry'

/**
 * Flutter Storage settings editor for FLUTTER_STORAGE_BASE_URL mirrors.
 *
 * Lets admins configure — per storage category (flutter_infra_release, flutter_infra,
 * download.flutter.io, dart-archive) — the upstream repository, allowed prefixes, manifest TTL,
 * manifest URL rewriting, and whether the category is served at all, plus the release manifest
 * base URL used to build the live per-version SDK registry.
 */

const props = defineProps<{
  /** Instance.Config map for the Flutter registry (mutated in place through v-model:config). */
  config: Record<string, any>
  /** Registry-level upstream URL, used as the inherited default for every category. */
  upstreamUrl?: string
  /** Compact mode omits descriptions (used inside the create/edit modal). */
  compact?: boolean
  /**
   * "instance" (default) edits a single registry's config.
   * "base" edits the platform-wide default Flutter Storage settings.
   */
  mode?: 'instance' | 'base'
  /**
   * Field keys supplied by the platform base settings while editing an instance. Values that are
   * not set here fall back to the base at runtime, so the editor badges them as inherited.
   */
  baseKeys?: string[]
  /** Base config values, used to show what an unset field will resolve to. */
  baseConfig?: Record<string, any>
}>()

const emit = defineEmits<{
  (e: 'update:config', value: Record<string, any>): void
}>()

const CATEGORY_META: Record<string, { label: string; hint: string; sample: string }> = {
  flutter_infra_release: {
    label: 'Flutter SDK Releases',
    hint: 'SDK tarballs, release manifests (releases_*.json), and engine artifacts served under /flutter_infra_release.',
    sample: 'flutter_infra_release/releases/releases_linux.json',
  },
  flutter_infra: {
    label: 'Flutter Infra / Engine',
    hint: 'Engine debug symbols, prebuilt binaries, and engine_manifest.json served under /flutter_infra.',
    sample: 'flutter_infra/flutter/engine_manifest.json',
  },
  'download.flutter.io': {
    label: 'Gradle Maven (Engine JARs)',
    hint: 'Android engine embeddings fetched by the Flutter Gradle plugin from /download.flutter.io.',
    sample: 'download.flutter.io/io/flutter/flutter_embedding_debug/1.0.0/flutter_embedding_debug-1.0.0.pom',
  },
  'dart-archive': {
    label: 'Dart SDK Archive',
    hint: 'Dart SDK archives and release channels served under /dart-archive.',
    sample: 'dart-archive/channels/stable/release/latest/release-linux-x64.json',
  },
}

const availableCategories = computed<string[]>(() => {
  const seen = new Set<string>()
  const out: string[] = []
  const push = (name: unknown) => {
    const n = String(name || '').trim()
    if (!n || seen.has(n)) return
    seen.add(n)
    out.push(n)
  }
  for (const entry of storage.value) push(entry.name)
  const catalogue = props.config.available_storage_categories
  if (Array.isArray(catalogue)) catalogue.forEach(push)
  return out
})

const storage = computed<FlutterDownloadStorageSetting[]>(() => {
  const raw = props.config.download_storage
  if (Array.isArray(raw)) return raw as FlutterDownloadStorageSetting[]
  return []
})

const releasesBaseUrl = computed({
  get: () => String(props.config.releases_base_url || ''),
  set: (v: string) => patch({ releases_base_url: v.trim() }),
})

const storageName = computed({
  get: () => String(props.config.storage_name || ''),
  set: (v: string) => patch({ storage_name: v }),
})

const registryTtl = computed({
  get: () => props.config.registry_ttl_seconds ?? '',
  set: (v: any) => patch({ registry_ttl_seconds: v === '' ? '' : Number(v) }),
})

const manifestTtl = computed({
  get: () => props.config.manifest_ttl_seconds ?? '',
  set: (v: any) => patch({ manifest_ttl_seconds: v === '' ? '' : Number(v) }),
})

const rewriteManifests = computed({
  get: () => props.config.rewrite_manifest_urls !== false,
  set: (v: boolean) => patch({ rewrite_manifest_urls: v }),
})

const serveStale = computed({
  get: () => props.config.serve_stale_on_error !== false,
  set: (v: boolean) => patch({ serve_stale_on_error: v }),
})

const upstreamTimeout = computed({
  get: () => props.config.upstream_timeout_seconds ?? '',
  set: (v: any) => patch({ upstream_timeout_seconds: v === '' ? '' : Number(v) }),
})

const maxArtifactSize = computed({
  get: () => props.config.max_artifact_size_bytes ?? '',
  set: (v: any) => patch({ max_artifact_size_bytes: v === '' ? '' : Number(v) }),
})

function patch(partial: Record<string, any>) {
  emit('update:config', { ...props.config, ...partial })
}

function metaFor(name: string) {
  return (
    CATEGORY_META[name] ?? {
      label: name,
      hint: 'Custom FLUTTER_STORAGE_BASE_URL path prefix mirrored by this registry.',
      sample: `${name}/example-artifact`,
    }
  )
}

function replaceAll(next: FlutterDownloadStorageSetting[]) {
  patch({ download_storage: next })
}

function updateEntry(index: number, partial: Partial<FlutterDownloadStorageSetting>) {
  const next = storage.value.map((entry, i) => (i === index ? { ...entry, ...partial } : entry))
  replaceAll(next)
}

function entryEnabled(entry: FlutterDownloadStorageSetting): boolean {
  return entry.enabled !== false
}

function entryPrefixes(entry: FlutterDownloadStorageSetting): string {
  return (entry.allowed_prefixes || []).join(', ')
}

function setEntryPrefixes(index: number, raw: string) {
  const prefixes = raw
    .split(',')
    .map((p) => p.trim())
    .filter(Boolean)
  updateEntry(index, { allowed_prefixes: prefixes.length > 0 ? prefixes : undefined })
}

function entryUpstream(entry: FlutterDownloadStorageSetting): string {
  return entry.upstream_url || ''
}

function inheritedUpstream(entry: FlutterDownloadStorageSetting): string {
  return entry.upstream_url || props.upstreamUrl || 'registry upstream'
}

function addCategory() {
  const candidate = window.prompt(
    'New storage category path prefix (e.g. flutter_infra_release_eu or my-internal-mirror):',
  )
  const name = (candidate || '').trim().replace(/^\/+|\/+$/g, '')
  if (!name) return
  if (storage.value.some((entry) => entry.name === name)) {
    window.alert(`Storage category "${name}" already exists.`)
    return
  }
  replaceAll([...storage.value, { name }])
}

function removeCategory(index: number) {
  const entry = storage.value[index]
  if (!entry) return
  if (!window.confirm(`Remove storage category "${entry.name}" from this registry?`)) return
  replaceAll(storage.value.filter((_, i) => i !== index))
}

function resetCategories() {
  const defaults = availableCategories.value.map((name) => ({ name }))
  replaceAll(defaults)
}

const isBaseMode = computed(() => props.mode === 'base')

const rootCompatHint = computed(() => {
  if (isBaseMode.value) {
    return props.upstreamUrl
      ? `Categories without their own upstream use the base upstream (${props.upstreamUrl}).`
      : 'Categories without their own upstream use the base upstream.'
  }
  return props.upstreamUrl
    ? `Unset categories inherit the registry upstream (${props.upstreamUrl}).`
    : 'Unset categories inherit the registry upstream.'
})

/** Keys present in config, used to badge which values the base supplies. */
const configuredKeys = computed(() => new Set(Object.keys(props.config || {}).filter((k) => props.config[k] !== undefined && props.config[k] !== null && props.config[k] !== '')))

function inheritsFromBase(key: string): boolean {
  if (isBaseMode.value) return false
  if (!props.baseKeys?.includes(key)) return false
  return !configuredKeys.value.has(key)
}

function baseValueHint(key: string): string {
  const v = props.baseConfig?.[key]
  if (v === undefined || v === null || v === '') return ''
  if (Array.isArray(v)) return `${v.length} entries`
  if (typeof v === 'boolean') return v ? 'enabled' : 'disabled'
  if (typeof v === 'object') return 'configured'
  return String(v)
}
</script>

<template>
  <div class="space-y-5">
    <!-- Storage identity & release manifests -->
    <div class="rounded-lg border bg-muted/15 p-4 space-y-4">
      <div class="flex items-center gap-2">
        <ServerCog class="h-4 w-4 text-primary" />
        <div>
          <h3 class="text-xs font-semibold uppercase tracking-wider">
            {{ isBaseMode ? 'Base Flutter Storage Defaults' : 'Flutter Storage Identity' }}
          </h3>
          <p v-if="!compact" class="text-[11px] text-muted-foreground">
            <template v-if="isBaseMode">
              Platform-wide defaults for every Flutter Storage registry: seeded into new registries, pushable
              to existing ones, and (in inheritance mode) applied to any field a registry leaves unset.
            </template>
            <template v-else>
              Name this FLUTTER_STORAGE_BASE_URL registry and choose where release manifests are fetched from.
            </template>
          </p>
        </div>
      </div>

      <div class="grid grid-cols-1 sm:grid-cols-2 gap-3.5">
        <div class="space-y-1">
          <label class="flex items-center gap-1.5 text-xs font-medium">
            Flutter Storage Name
            <UiBadge v-if="inheritsFromBase('storage_name')" variant="secondary" class="text-[10px]">
              base: {{ baseValueHint('storage_name') }}
            </UiBadge>
          </label>
          <UiInput v-model="storageName" placeholder="e.g. Flutter Storage (CN Mirror)" />
          <p v-if="!compact" class="text-[11px] text-muted-foreground">
            Shown in the Admin Console and the public Flutter Storage Explorer.
          </p>
        </div>

        <div class="space-y-1">
          <label class="flex items-center gap-1.5 text-xs font-medium">
            Release Manifest Base URL (optional)
            <UiBadge v-if="inheritsFromBase('releases_base_url')" variant="secondary" class="text-[10px]">
              base: {{ baseValueHint('releases_base_url') }}
            </UiBadge>
          </label>
          <UiInput
            v-model="releasesBaseUrl"
            type="url"
            placeholder="https://storage.googleapis.com/flutter_infra_release/releases"
          />
          <p v-if="!compact" class="text-[11px] text-muted-foreground">
            Where releases_*.json manifests are read from when building the per-version registry. Empty =
            &lt;upstream&gt;/flutter_infra_release/releases.
          </p>
        </div>

        <div class="space-y-1">
          <label class="flex items-center gap-1.5 text-xs font-medium">
            Version Registry TTL (seconds)
            <UiBadge v-if="inheritsFromBase('registry_ttl_seconds')" variant="secondary" class="text-[10px]">
              base: {{ baseValueHint('registry_ttl_seconds') }}
            </UiBadge>
          </label>
          <UiInput v-model="registryTtl" type="number" min="0" max="86400" placeholder="900" />
        </div>

        <div class="space-y-1">
          <label class="flex items-center gap-1.5 text-xs font-medium">
            Default Manifest TTL (seconds)
            <UiBadge v-if="inheritsFromBase('manifest_ttl_seconds')" variant="secondary" class="text-[10px]">
              base: {{ baseValueHint('manifest_ttl_seconds') }}
            </UiBadge>
          </label>
          <UiInput v-model="manifestTtl" type="number" min="0" max="86400" placeholder="300" />
        </div>
      </div>

      <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <div class="flex items-center justify-between gap-3 rounded-md border bg-background px-3 py-2">
          <div>
            <div class="text-xs font-medium">Rewrite Manifest base_url</div>
            <div class="text-[11px] text-muted-foreground">
              Point the Flutter CLI at this mirror by default.
            </div>
          </div>
          <UiSwitch v-model="rewriteManifests" aria-label="Rewrite manifest base_url" />
        </div>

        <div class="flex items-center justify-between gap-3 rounded-md border bg-background px-3 py-2">
          <div>
            <div class="text-xs font-medium">Serve Stale on Upstream Error</div>
            <div class="text-[11px] text-muted-foreground">
              Keep handing out cached manifests when upstream is down.
            </div>
          </div>
          <UiSwitch v-model="serveStale" aria-label="Serve stale cache on upstream error" />
        </div>
      </div>

      <div class="grid grid-cols-1 sm:grid-cols-2 gap-3.5">
        <div class="space-y-1">
          <label class="text-xs font-medium">Upstream Download Timeout (seconds)</label>
          <UiInput v-model="upstreamTimeout" type="number" min="0" max="3600" placeholder="300" />
        </div>
        <div class="space-y-1">
          <label class="text-xs font-medium">Max Artifact Size (bytes)</label>
          <UiInput v-model="maxArtifactSize" type="number" min="0" placeholder="2147483648" />
        </div>
      </div>
    </div>

    <!-- Per-category storage settings -->
    <div class="rounded-lg border p-4 space-y-3">
      <div class="flex flex-wrap items-start justify-between gap-2">
        <div class="flex items-center gap-2">
          <Globe2 class="h-4 w-4 text-primary" />
          <div>
            <h3 class="text-xs font-semibold uppercase tracking-wider">Storage Category Settings</h3>
            <p v-if="!compact" class="text-[11px] text-muted-foreground">
              Each FLUTTER_STORAGE_BASE_URL category can point at its own upstream repository and cache policy.
              {{ rootCompatHint }}
            </p>
          </div>
        </div>
        <div class="flex items-center gap-1.5">
          <UiButton type="button" variant="outline" size="sm" @click="resetCategories">
            <RotateCcw class="h-3 w-3" />
            Defaults
          </UiButton>
          <UiButton type="button" variant="outline" size="sm" @click="addCategory">
            <Plus class="h-3 w-3" />
            Add Category
          </UiButton>
        </div>
      </div>

      <div v-if="storage.length === 0" class="rounded-md border border-dashed px-3 py-6 text-center text-xs text-muted-foreground">
        No storage categories configured. Saving will restore the built-in Flutter categories.
      </div>

      <div v-for="(entry, index) in storage" :key="entry.name" class="rounded-md border bg-muted/10 p-3 space-y-3">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div class="flex items-center gap-2">
            <UiBadge variant="info" class="font-mono text-[10px]">/{{ entry.name }}</UiBadge>
            <span class="text-xs font-medium">{{ metaFor(entry.name).label }}</span>
          </div>
          <div class="flex items-center gap-2">
            <div class="flex items-center gap-1.5">
              <span class="text-[11px] text-muted-foreground">{{ entryEnabled(entry) ? 'Enabled' : 'Disabled' }}</span>
              <UiSwitch
                :model-value="entryEnabled(entry)"
                :aria-label="`Enable ${entry.name}`"
                @update:model-value="(v) => updateEntry(index, { enabled: Boolean(v) })"
              />
            </div>
            <button
              type="button"
              class="inline-flex h-8 w-8 items-center justify-center rounded-md border text-muted-foreground hover:bg-red-50 hover:text-red-600"
              :aria-label="`Remove ${entry.name}`"
              @click="removeCategory(index)"
            >
              <Trash2 class="h-3.5 w-3.5" />
            </button>
          </div>
        </div>

        <p v-if="!compact" class="text-[11px] text-muted-foreground">{{ metaFor(entry.name).hint }}</p>

        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3.5">
          <div class="space-y-1">
            <label class="text-xs font-medium">Upstream Repository URL</label>
            <UiInput
              :model-value="entryUpstream(entry)"
              type="url"
              :placeholder="`Inherit registry upstream (${props.upstreamUrl || 'not set'})`"
              @update:model-value="(v) => updateEntry(index, { upstream_url: String(v).trim() })"
            />
            <p class="text-[11px] text-muted-foreground font-mono truncate">
              Effective: {{ inheritedUpstream(entry) }}
            </p>
          </div>

          <div class="space-y-1">
            <label class="text-xs font-medium">Allowed Prefixes</label>
            <UiInput
              :model-value="entryPrefixes(entry)"
              :placeholder="entry.name"
              @update:model-value="(v) => setEntryPrefixes(index, String(v))"
            />
            <p class="text-[11px] text-muted-foreground font-mono truncate">
              Example: {{ metaFor(entry.name).sample }}
            </p>
          </div>

          <div class="space-y-1">
            <label class="text-xs font-medium">Category Manifest TTL (seconds, 0 = inherit)</label>
            <UiInput
              :model-value="entry.manifest_ttl_seconds ?? ''"
              type="number"
              min="0"
              max="86400"
              :placeholder="String(props.config.manifest_ttl_seconds ?? 300)"
              @update:model-value="
                (v) =>
                  updateEntry(index, {
                    manifest_ttl_seconds: v === '' ? undefined : Number(v),
                  })
              "
            />
          </div>

          <div class="flex items-center justify-between gap-3 rounded-md border bg-background px-3 py-2">
            <div>
              <div class="text-xs font-medium">Rewrite base_url for this category</div>
              <div class="text-[11px] text-muted-foreground">Inherit global setting when off.</div>
            </div>
            <UiSwitch
              :model-value="entry.rewrite_manifest_urls !== false"
              :aria-label="`Rewrite base_url for ${entry.name}`"
              @update:model-value="(v) => updateEntry(index, { rewrite_manifest_urls: Boolean(v) })"
            />
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
