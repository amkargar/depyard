<script setup lang="ts">
import { Activity, Plus, Trash2, CheckCircle2, XCircle, Lock } from 'lucide-vue-next'
import type {
  PlatformSettings,
  RegistryInstance,
  RegistryTypeInfo,
  RepositoryMode,
  UpstreamAuthType,
  UpstreamConfig,
  UpstreamHealth,
  Visibility,
} from '~/types/registry'

const props = defineProps<{
  open: boolean
  types: RegistryTypeInfo[]
  editing?: RegistryInstance | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'saved', inst: RegistryInstance): void
}>()

interface HeaderPair {
  key: string
  value: string
}

const saving = ref(false)
const testing = ref(false)
const errorMsg = ref('')
const testResult = ref<UpstreamHealth | null>(null)
const testError = ref('')
const platformDefaults = ref<{ pub: string; flutter: string }>({
  pub: 'https://pub.dev',
  flutter: 'https://storage.googleapis.com',
})
/** Platform base Flutter Storage settings, used to seed new registries. */
const baseFlutterConfig = ref<Record<string, any>>({})
const baseFlutterMode = ref<'defaults' | 'inherit'>('defaults')
const useBaseFlutter = ref(true)

/** True when a base config exists and the admin wants to start from it. */
const willSeedFromBase = computed(
  () => isFlutterType.value && useBaseFlutter.value && Object.keys(baseFlutterConfig.value).length > 0,
)

const form = reactive({
  name: '',
  slug: '',
  type: '',
  description: '',
  enabled: true,
  mode: 'hybrid' as RepositoryMode,
  upstream_url: 'https://pub.dev',
  upstream_auth_type: '' as UpstreamAuthType,
  upstream_auth_header: '',
  upstream_auth_username: '',
  upstream_auth_token: '',
  upstream_has_auth_secret: false,
  upstream_proxy_url: '',
  upstream_timeout_seconds: 0,
  upstream_insecure_skip_verify: false,
  upstream_custom_headers: [] as HeaderPair[],
  visibility: 'public' as Visibility,
  allow_anonymous_read: true,
  require_auth_publish: true,
  quota_mb: 0,
  metadata_ttl_seconds: 300,
  archive_ttl_seconds: 0,
  cache_max_mb: 0,
  negative_cache_ttl_seconds: 60,
  eviction_policy: 'lru',
  is_default: false,
  hostname: '',
  config: {} as Record<string, any>,
})

const selectedType = computed(
  () => props.types.find((t) => t.type === form.type) ?? props.types[0],
)

const supportedModes = computed<RepositoryMode[]>(
  () => selectedType.value?.supported_modes ?? ['proxy', 'hosted', 'hybrid'],
)

const needsUpstream = computed(() => form.mode === 'proxy' || form.mode === 'hybrid')

// --- Dropdown option lists (the custom UiSelect takes plain option arrays) ---

const typeOptions = computed(() =>
  props.types.map((t) => ({
    value: t.type,
    label: `${t.display_name} (${t.type})`,
  })),
)

const MODE_LABELS: Record<string, string> = {
  proxy: 'Proxy (Upstream Cache)',
  hosted: 'Hosted (Local Publish)',
  hybrid: 'Hybrid (Hosted + Upstream Proxy)',
}

const modeOptions = computed(() =>
  supportedModes.value.map((m) => ({
    value: m,
    label: MODE_LABELS[m] ?? m,
  })),
)

const upstreamAuthOptions = computed(() => [
  { value: '', label: 'None (Anonymous)' },
  { value: 'bearer', label: 'Bearer Token' },
  { value: 'basic', label: 'HTTP Basic Auth' },
  { value: 'header', label: 'Custom Auth Header' },
])

const visibilityOptions = computed(() => [
  { value: 'public', label: 'Public' },
  { value: 'private', label: 'Private (Token / Auth Required)' },
])

const evictionPolicyOptions = computed(() => [
  { value: 'lru', label: 'LRU (Least Recently Used)' },
  { value: 'lfu', label: 'LFU (Least Frequently Used)' },
])

function defaultUpstreamForType(regType: string): string {
  if (regType === 'flutter') return platformDefaults.value.flutter || 'https://storage.googleapis.com'
  if (regType === 'pub') return platformDefaults.value.pub || 'https://pub.dev'
  return ''
}

function applyTypeConfigDefaults(regType?: RegistryTypeInfo) {
  form.config = {}
  if (regType?.config_hints) {
    for (const h of regType.config_hints) {
      if (h.default_value !== undefined) {
        form.config[h.key] = Array.isArray(h.default_value)
          ? [...h.default_value]
          : h.default_value
      }
    }
  }
  if (regType?.type === 'flutter') {
    if (useBaseFlutter.value && Object.keys(baseFlutterConfig.value).length > 0) {
      // Seed from the platform base settings so a new registry starts from org-wide policy.
      const seeded: Record<string, any> = {}
      for (const [key, value] of Object.entries(baseFlutterConfig.value)) {
        if (key === 'upstream_url') continue // base-only: goes to Instance.UpstreamURL
        seeded[key] = Array.isArray(value) ? JSON.parse(JSON.stringify(value)) : value
      }
      form.config = { ...form.config, ...seeded }
      if (baseFlutterMode.value === 'inherit') {
        // In inherit mode the base already supplies these live; persisting them would make the
        // registry look "overridden" and freeze today's values. Keep only the catalogue.
        form.config = { available_storage_categories: form.config.available_storage_categories }
      }
    }
    const catalogue: string[] = Array.isArray(form.config.available_storage_categories)
      ? form.config.available_storage_categories
      : []
    if (!Array.isArray(form.config.download_storage) || form.config.download_storage.length === 0) {
      form.config.download_storage = catalogue.map((name) => ({ name }))
    }
    if (!form.config.storage_name) form.config.storage_name = 'Flutter Storage'
  }
}

/** Config keys managed by the dedicated Flutter Storage settings editor. */
const FLUTTER_STORAGE_KEYS = new Set([
  'storage_name',
  'available_storage_categories',
  'releases_base_url',
  'registry_ttl_seconds',
  'download_storage',
  'manifest_ttl_seconds',
  'rewrite_manifest_urls',
  'serve_stale_on_error',
  'upstream_timeout_seconds',
  'max_artifact_size_bytes',
])

const isFlutterType = computed(() => form.type === 'flutter')

// Hints rendered by the generic hint renderer (Flutter uses its dedicated editor instead).
const genericConfigHints = computed(() =>
  (selectedType.value?.config_hints ?? []).filter(
    (hint) => !(isFlutterType.value && FLUTTER_STORAGE_KEYS.has(hint.key)),
  ),
)

function handleFlutterConfigUpdate(next: Record<string, any>) {
  form.config = { ...next }
}

async function fetchPlatformDefaults() {
  try {
    const s = await apiFetch<PlatformSettings>('/api/admin/settings')
    platformDefaults.value = {
      pub: s.default_pub_upstream_url || 'https://pub.dev',
      flutter: s.default_flutter_storage_url || 'https://storage.googleapis.com',
    }
    baseFlutterConfig.value = s.default_flutter_storage_config || {}
    baseFlutterMode.value = s.flutter_base_config_mode === 'inherit' ? 'inherit' : 'defaults'
    if (s.default_flutter_storage_config?.upstream_url) {
      platformDefaults.value.flutter = s.default_flutter_storage_config.upstream_url
    }
  } catch {
    // fallback defaults already set
  }
}

watch(
  () => [props.open, props.editing, props.types],
  async () => {
    if (!props.open) return
    errorMsg.value = ''
    testResult.value = null
    testError.value = ''

    if (!props.editing) {
      await fetchPlatformDefaults()
    }

    if (props.editing) {
      form.name = props.editing.name
      form.slug = props.editing.slug
      form.type = props.editing.type
      form.description = props.editing.description || ''
      form.enabled = props.editing.enabled
      form.mode = props.editing.mode
      form.upstream_url = props.editing.upstream_url || ''
      const uc = props.editing.upstream_config
      form.upstream_auth_type = (uc?.auth_type as UpstreamAuthType) || ''
      form.upstream_auth_header = uc?.auth_header || ''
      form.upstream_auth_username = uc?.auth_username || ''
      form.upstream_auth_token = ''
      form.upstream_has_auth_secret = Boolean(uc?.has_auth_secret)
      form.upstream_proxy_url = uc?.proxy_url || ''
      form.upstream_timeout_seconds = uc?.timeout_seconds || 0
      form.upstream_insecure_skip_verify = Boolean(uc?.insecure_skip_verify)
      form.upstream_custom_headers = Object.entries(uc?.custom_headers || {}).map(([key, value]) => ({
        key,
        value,
      }))
      form.visibility = props.editing.visibility
      form.allow_anonymous_read = props.editing.auth_policy?.allow_anonymous_read ?? true
      form.require_auth_publish = props.editing.auth_policy?.require_auth_publish ?? true
      form.quota_mb = props.editing.storage_quota_bytes
        ? Math.round(props.editing.storage_quota_bytes / (1024 * 1024))
        : 0
      form.metadata_ttl_seconds = props.editing.cache_policy?.metadata_ttl_seconds ?? 300
      form.archive_ttl_seconds = props.editing.cache_policy?.archive_ttl_seconds ?? 0
      form.cache_max_mb = props.editing.cache_policy?.max_size_bytes
        ? Math.round(props.editing.cache_policy.max_size_bytes / (1024 * 1024))
        : 0
      form.negative_cache_ttl_seconds = props.editing.cache_policy?.negative_cache_ttl_seconds ?? 60
      form.eviction_policy = props.editing.cache_policy?.eviction_policy || 'lru'
      form.is_default = props.editing.is_default
      form.hostname = props.editing.hostname || ''
      form.config = { ...(props.editing.config || {}) }
    } else {
      const defType = props.types[0]
      form.name = ''
      form.slug = ''
      form.type = defType?.type || 'pub'
      form.description = ''
      form.enabled = true
      form.mode = defType?.supported_modes?.includes('hybrid')
        ? 'hybrid'
        : defType?.supported_modes?.[0] || 'proxy'
      form.upstream_url = defaultUpstreamForType(form.type)
      form.upstream_auth_type = ''
      form.upstream_auth_header = ''
      form.upstream_auth_username = ''
      form.upstream_auth_token = ''
      form.upstream_has_auth_secret = false
      form.upstream_proxy_url = ''
      form.upstream_timeout_seconds = 0
      form.upstream_insecure_skip_verify = false
      form.upstream_custom_headers = []
      form.visibility = 'public'
      form.allow_anonymous_read = true
      form.require_auth_publish = true
      form.quota_mb = 0
      form.metadata_ttl_seconds = 300
      form.archive_ttl_seconds = 0
      form.cache_max_mb = 0
      form.negative_cache_ttl_seconds = 60
      form.eviction_policy = 'lru'
      form.is_default = false
      form.hostname = ''
      applyTypeConfigDefaults(defType)
    }
  },
  { immediate: true },
)

watch(
  () => form.type,
  (newType) => {
    if (props.editing) return
    const typeInfo = props.types.find((t) => t.type === newType)
    const modes = typeInfo?.supported_modes ?? []
    if (!modes.includes(form.mode) && modes.length > 0) {
      form.mode = modes[0]
    }
    form.upstream_url = defaultUpstreamForType(newType)
    applyTypeConfigDefaults(typeInfo)
    testResult.value = null
    testError.value = ''
  },
)

// Base settings may arrive after the create watch ran; re-apply the Flutter defaults once they do.
watch(baseFlutterConfig, () => {
  if (props.editing || !props.open || !isFlutterType.value) return
  if (form.name || form.slug) return // admin already typed something; never stomp on it
  applyTypeConfigDefaults(props.types.find((t) => t.type === form.type))
})

watch(
  () => form.visibility,
  (vis) => {
    if (vis === 'private') {
      form.allow_anonymous_read = false
    }
  },
)

function slugifyName() {
  if (props.editing || form.slug) return
  form.slug = form.name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

function getListConfigString(key: string): string {
  const val = form.config[key]
  if (Array.isArray(val)) return val.join(', ')
  return typeof val === 'string' ? val : ''
}

function setListConfigString(key: string, raw: string) {
  const items = raw
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
  if (items.length === 0) {
    delete form.config[key]
  } else {
    form.config[key] = items
  }
}

function addHeaderRow() {
  form.upstream_custom_headers.push({ key: '', value: '' })
}

function removeHeaderRow(idx: number) {
  form.upstream_custom_headers.splice(idx, 1)
}

function buildUpstreamConfigPayload(): UpstreamConfig {
  const headers: Record<string, string> = {}
  for (const pair of form.upstream_custom_headers) {
    const k = pair.key.trim()
    const v = pair.value.trim()
    if (k && v) headers[k] = v
  }
  const authType = form.upstream_auth_type === 'none' ? '' : form.upstream_auth_type
  return {
    auth_type: authType,
    auth_header: authType === 'header' ? form.upstream_auth_header.trim() : '',
    auth_username: authType === 'basic' ? form.upstream_auth_username.trim() : '',
    auth_token: authType ? form.upstream_auth_token : '',
    proxy_url: form.upstream_proxy_url.trim(),
    timeout_seconds: Math.max(0, Number(form.upstream_timeout_seconds) || 0),
    insecure_skip_verify: form.upstream_insecure_skip_verify,
    custom_headers: Object.keys(headers).length > 0 ? headers : undefined,
  }
}

async function handleTestUpstream() {
  if (!form.upstream_url.trim()) return
  testing.value = true
  testResult.value = null
  testError.value = ''
  try {
    let targetUrl = form.upstream_url.trim()
    if (form.type === 'flutter') {
      targetUrl = `${targetUrl.replace(/\/$/, '')}/flutter_infra_release/releases/releases_linux.json`
    }
    testResult.value = await apiFetch<UpstreamHealth>('/api/admin/upstream/test', {
      method: 'POST',
      body: JSON.stringify({
        url: targetUrl,
        registry_id: props.editing?.id || '',
        upstream_config: buildUpstreamConfigPayload(),
      }),
    })
  } catch (err: any) {
    testError.value = err?.message || 'Upstream connectivity test failed'
  } finally {
    testing.value = false
  }
}

async function handleSubmit() {
  saving.value = true
  errorMsg.value = ''
  try {
    const cleanConfig: Record<string, any> = {}
    if (selectedType.value?.config_hints) {
      for (const h of selectedType.value.config_hints) {
        const val = form.config[h.key]
        if (val === undefined || val === '' || (Array.isArray(val) && val.length === 0)) {
          continue
        }
        if (h.key === 'available_storage_categories') continue // read-only catalogue
        if (h.type === 'number') {
          cleanConfig[h.key] = Number(val)
        } else {
          cleanConfig[h.key] = val
        }
      }
    }
    if (isFlutterType.value) {
      const storageEntries = Array.isArray(form.config.download_storage)
        ? (form.config.download_storage as any[])
            .map((entry) => ({
              name: String(entry?.name || '').trim(),
              upstream_url: String(entry?.upstream_url || '').trim(),
              allowed_prefixes: Array.isArray(entry?.allowed_prefixes)
                ? entry.allowed_prefixes
                : undefined,
              manifest_ttl_seconds:
                entry?.manifest_ttl_seconds === undefined || entry?.manifest_ttl_seconds === ''
                  ? undefined
                  : Number(entry.manifest_ttl_seconds),
              rewrite_manifest_urls:
                typeof entry?.rewrite_manifest_urls === 'boolean'
                  ? entry.rewrite_manifest_urls
                  : undefined,
              enabled: typeof entry?.enabled === 'boolean' ? entry.enabled : undefined,
            }))
            .filter((entry) => entry.name)
        : []
      if (storageEntries.length > 0) {
        cleanConfig.download_storage = storageEntries
      } else {
        delete cleanConfig.download_storage
      }
    }

    const payload = {
      name: form.name.trim(),
      slug: form.slug.trim().toLowerCase(),
      type: form.type,
      description: form.description.trim(),
      enabled: form.enabled,
      mode: form.mode,
      upstream_url: needsUpstream.value ? form.upstream_url.trim() : '',
      upstream_config: needsUpstream.value ? buildUpstreamConfigPayload() : undefined,
      visibility: form.visibility,
      auth_policy: {
        allow_anonymous_read: form.visibility === 'public' && form.allow_anonymous_read,
        require_auth_publish: form.require_auth_publish,
      },
      storage_quota_bytes: Math.max(0, Math.round(Number(form.quota_mb) || 0)) * 1024 * 1024,
      cache_policy: {
        metadata_ttl_seconds: Math.max(0, Number(form.metadata_ttl_seconds) || 0),
        archive_ttl_seconds: Math.max(0, Number(form.archive_ttl_seconds) || 0),
        max_size_bytes: Math.max(0, Math.round(Number(form.cache_max_mb) || 0)) * 1024 * 1024,
        eviction_policy: form.eviction_policy || 'lru',
        negative_cache_ttl_seconds: Math.max(0, Number(form.negative_cache_ttl_seconds) || 0),
      },
      config: cleanConfig,
      is_default: form.is_default,
      hostname: form.hostname.trim(),
    }

    const saved = props.editing
      ? await apiFetch<RegistryInstance>(`/api/admin/registries/${props.editing.id}`, {
          method: 'PUT',
          body: JSON.stringify(payload),
        })
      : await apiFetch<RegistryInstance>('/api/admin/registries', {
          method: 'POST',
          body: JSON.stringify(payload),
        })

    emit('saved', saved)
    emit('close')
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to save registry'
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <UiDialog
    :open="open"
    :title="editing ? `Configure ${editing.name}` : 'Create Registry Instance'"
    description="Changes apply live to protocol routing, upstream connections, and caching without restarting the server."
    max-width-class="max-w-3xl"
    @close="emit('close')"
  >
    <form id="registry-form" class="space-y-5" @submit.prevent="handleSubmit">
      <SharedErrorBanner :message="errorMsg" />

      <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
        <div class="space-y-1.5">
          <label class="text-xs font-medium text-foreground">Registry Type</label>
          <UiSelect
            v-model="form.type"
            :disabled="Boolean(editing)"
            :options="typeOptions"
            aria-label="Registry type"
          />
          <p class="text-[11px] text-muted-foreground">
            Only registry types compiled into this server are selectable.
          </p>
        </div>

        <div class="space-y-1.5">
          <label class="text-xs font-medium text-foreground">Repository Mode</label>
          <UiSelect
            v-model="form.mode"
            :options="modeOptions"
            aria-label="Repository mode"
          />
        </div>
      </div>

      <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
        <div class="space-y-1.5">
          <label class="text-xs font-medium text-foreground">Display Name</label>
          <UiInput
            v-model="form.name"
            placeholder="e.g. Flutter Storage Mirror"
            required
            @blur="slugifyName"
          />
        </div>
        <div class="space-y-1.5">
          <label class="text-xs font-medium text-foreground">Slug (URL path)</label>
          <UiInput
            v-model="form.slug"
            placeholder="e.g. flutter-storage"
            :disabled="Boolean(editing)"
            required
          />
          <p class="text-[11px] text-muted-foreground font-mono">
            Base URL: /r/{{ form.slug || 'your-slug' }}
          </p>
        </div>
      </div>

      <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
        <div class="space-y-1.5">
          <label class="text-xs font-medium text-foreground">Description</label>
          <UiInput v-model="form.description" placeholder="Short description of this registry" />
        </div>
        <div class="space-y-1.5">
          <label class="text-xs font-medium text-foreground">Custom Hostname (optional)</label>
          <UiInput v-model="form.hostname" placeholder="e.g. flutter.mirror.internal" />
        </div>
      </div>

      <!-- UPSTREAM CONFIGURATION -->
      <div v-if="needsUpstream" class="space-y-4 rounded-lg border bg-muted/15 p-4">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div>
            <h3 class="text-xs font-semibold uppercase tracking-wider text-foreground">
              Upstream Connection &amp; Authentication
            </h3>
            <p class="text-[11px] text-muted-foreground">
              Configure the upstream mirror URL, optional authentication, corporate proxy override, TLS verification, and custom headers.
            </p>
          </div>
          <UiButton
            type="button"
            variant="outline"
            size="sm"
            :disabled="testing || !form.upstream_url.trim()"
            @click="handleTestUpstream"
          >
            <Activity class="h-3.5 w-3.5" />
            {{ testing ? 'Testing...' : 'Test Connection' }}
          </UiButton>
        </div>

        <div class="space-y-1.5">
          <label class="text-xs font-medium text-foreground">Upstream URL</label>
          <UiInput
            v-model="form.upstream_url"
            type="url"
            :placeholder="form.type === 'flutter' ? 'https://storage.googleapis.com' : 'https://pub.dev'"
            required
          />
        </div>

        <div
          v-if="testResult || testError"
          :class="[
            'flex items-center gap-2 rounded-md border px-3 py-2 text-xs',
            testResult?.available
              ? 'border-emerald-200 bg-emerald-50 text-emerald-900'
              : 'border-red-200 bg-red-50 text-red-900',
          ]"
        >
          <CheckCircle2 v-if="testResult?.available" class="h-4 w-4 text-emerald-600 shrink-0" />
          <XCircle v-else class="h-4 w-4 text-red-600 shrink-0" />
          <span v-if="testError">{{ testError }}</span>
          <span v-else-if="testResult">
            {{ testResult.available ? 'Reachable' : 'Unreachable' }}
            <template v-if="testResult.status_code"> · HTTP {{ testResult.status_code }}</template>
            · {{ testResult.latency_ms }} ms
            <template v-if="testResult.error"> — {{ testResult.error }}</template>
          </span>
        </div>

        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3.5">
          <div class="space-y-1">
            <label class="text-xs font-medium">Authentication Mode</label>
            <UiSelect
              v-model="form.upstream_auth_type"
              :options="upstreamAuthOptions"
              aria-label="Upstream authentication type"
            />
          </div>

          <div v-if="form.upstream_auth_type === 'basic'" class="space-y-1">
            <label class="text-xs font-medium">Username</label>
            <UiInput v-model="form.upstream_auth_username" placeholder="username" />
          </div>

          <div v-if="form.upstream_auth_type === 'header'" class="space-y-1">
            <label class="text-xs font-medium">Header Name</label>
            <UiInput v-model="form.upstream_auth_header" placeholder="e.g. X-API-Key" />
          </div>

          <div v-if="form.upstream_auth_type && form.upstream_auth_type !== 'none'" class="space-y-1 sm:col-span-2">
            <label class="text-xs font-medium flex items-center gap-1.5">
              {{ form.upstream_auth_type === 'basic' ? 'Password' : 'Token / Secret Value' }}
              <span
                v-if="form.upstream_has_auth_secret"
                class="inline-flex items-center gap-1 rounded bg-emerald-100 px-1.5 py-0.5 text-[10px] font-medium text-emerald-800"
              >
                <Lock class="h-2.5 w-2.5" />
                Stored
              </span>
            </label>
            <UiInput
              v-model="form.upstream_auth_token"
              type="password"
              autocomplete="new-password"
              :placeholder="
                form.upstream_has_auth_secret
                  ? 'Leave blank to keep stored secret, or type a new value to rotate'
                  : 'Enter upstream credential'
              "
            />
          </div>
        </div>

        <div class="grid grid-cols-1 sm:grid-cols-3 gap-3.5">
          <div class="space-y-1 sm:col-span-2">
            <label class="text-xs font-medium">Per-Registry Proxy Override (optional)</label>
            <UiInput
              v-model="form.upstream_proxy_url"
              placeholder="Leave blank to inherit global proxy (http://... or socks5://...)"
            />
          </div>

          <div class="space-y-1">
            <label class="text-xs font-medium">Timeout Override (sec, 0=default)</label>
            <UiInput v-model="form.upstream_timeout_seconds" type="number" min="0" max="3600" />
          </div>
        </div>

        <div class="flex items-center justify-between rounded-md border bg-background px-3 py-2">
          <div>
            <div class="text-xs font-medium">Skip TLS Certificate Verification</div>
            <div class="text-[11px] text-muted-foreground">
              Allow self-signed TLS certificates on this upstream server
            </div>
          </div>
          <UiSwitch
            v-model="form.upstream_insecure_skip_verify"
            aria-label="Skip TLS certificate verification for this registry"
          />
        </div>

        <!-- Custom Upstream Headers -->
        <div class="space-y-2">
          <div class="flex items-center justify-between">
            <label class="text-xs font-medium">Custom Upstream Headers (optional)</label>
            <button
              type="button"
              class="inline-flex items-center gap-1 text-xs font-medium text-primary hover:underline"
              @click="addHeaderRow"
            >
              <Plus class="h-3 w-3" />
              Add Header
            </button>
          </div>
          <div v-for="(pair, idx) in form.upstream_custom_headers" :key="idx" class="flex items-center gap-2">
            <UiInput v-model="pair.key" placeholder="Header-Name" class="flex-1" />
            <UiInput v-model="pair.value" placeholder="Value" class="flex-1" />
            <button
              type="button"
              class="inline-flex h-9 w-9 items-center justify-center rounded-md border text-muted-foreground hover:bg-red-50 hover:text-red-600"
              @click="removeHeaderRow(idx)"
            >
              <Trash2 class="h-3.5 w-3.5" />
            </button>
          </div>
        </div>
      </div>

      <!-- Visibility & Quotas -->
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-4 pt-1">
        <div class="space-y-1.5">
          <label class="text-xs font-medium text-foreground">Visibility</label>
          <UiSelect
            v-model="form.visibility"
            :options="visibilityOptions"
            aria-label="Registry visibility"
          />
        </div>

        <div class="space-y-1.5">
          <label class="text-xs font-medium text-foreground">Storage Quota (MB, 0 = unlimited)</label>
          <UiInput v-model="form.quota_mb" type="number" min="0" />
        </div>
      </div>

      <div class="grid grid-cols-1 sm:grid-cols-2 gap-4 rounded-lg border bg-muted/20 p-3.5">
        <div class="flex items-center justify-between gap-3">
          <div>
            <div class="text-xs font-medium">Allow Anonymous Read</div>
            <div class="text-[11px] text-muted-foreground">Public repositories only</div>
          </div>
          <UiSwitch
            v-model="form.allow_anonymous_read"
            :disabled="form.visibility === 'private'"
            aria-label="Allow anonymous read"
          />
        </div>

        <div class="flex items-center justify-between gap-3">
          <div>
            <div class="text-xs font-medium">Default for {{ selectedType?.display_name || form.type }}</div>
            <div class="text-[11px] text-muted-foreground">Serves root compatibility routes for this type</div>
          </div>
          <UiSwitch v-model="form.is_default" aria-label="Default compatibility instance" />
        </div>
      </div>

      <!-- Cache Policy -->
      <div v-if="needsUpstream" class="space-y-3 border-t pt-4">
        <h3 class="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Cache Policy</h3>
        <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
          <div class="space-y-1">
            <label class="text-xs text-muted-foreground">Metadata TTL (sec)</label>
            <UiInput v-model="form.metadata_ttl_seconds" type="number" min="0" />
          </div>
          <div class="space-y-1">
            <label class="text-xs text-muted-foreground">Archive TTL (sec, 0=keep)</label>
            <UiInput v-model="form.archive_ttl_seconds" type="number" min="0" />
          </div>
          <div class="space-y-1">
            <label class="text-xs text-muted-foreground">Max Cache Size (MB, 0=unlimited)</label>
            <UiInput v-model="form.cache_max_mb" type="number" min="0" />
          </div>
          <div class="space-y-1">
            <label class="text-xs text-muted-foreground">Negative Cache TTL (sec)</label>
            <UiInput v-model="form.negative_cache_ttl_seconds" type="number" min="0" />
          </div>
          <div class="space-y-1">
            <label class="text-xs text-muted-foreground">Eviction Policy</label>
            <UiSelect
              v-model="form.eviction_policy"
              :options="evictionPolicyOptions"
              aria-label="Cache eviction policy"
            />
          </div>
        </div>
      </div>

      <!-- Flutter Storage (FLUTTER_STORAGE_BASE_URL) category settings -->
      <div v-if="isFlutterType" class="space-y-3 border-t pt-4">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <h3 class="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
            FLUTTER_STORAGE_BASE_URL Settings
          </h3>
          <div
            v-if="baseFlutterMode === 'inherit' && Object.keys(baseFlutterConfig).length > 0"
            class="flex items-center gap-2"
          >
            <UiBadge variant="secondary" class="text-[10px]">inherits base settings</UiBadge>
            <UiSwitch
              :model-value="useBaseFlutter"
              aria-label="Seed this registry from the base Flutter Storage settings"
              @update:model-value="
                (v: boolean) => {
                  useBaseFlutter = v
                  applyTypeConfigDefaults(types.find((t) => t.type === form.type))
                }
              "
            />
          </div>
        </div>
        <p v-if="willSeedFromBase" class="text-[11px] text-muted-foreground">
          Pre-filled from the platform base settings.
          <template v-if="baseFlutterMode === 'inherit'">
            Unset fields keep following the base live — only set a field here to override it.
          </template>
        </p>
        <SharedFlutterStorageSettings
          :config="form.config"
          :upstream-url="form.upstream_url"
          :base-keys="baseFlutterMode === 'inherit' ? Object.keys(baseFlutterConfig) : []"
          :base-config="baseFlutterConfig"
          compact
          @update:config="handleFlutterConfigUpdate"
        />
      </div>

      <!-- Ecosystem-specific validated settings rendered from TypeInfo hints -->
      <div v-if="genericConfigHints.length" class="space-y-3 border-t pt-4">
        <h3 class="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          {{ selectedType?.display_name }} Settings
        </h3>
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3.5">
          <div v-for="hint in genericConfigHints" :key="hint.key" class="space-y-1">
            <div v-if="hint.type === 'boolean'" class="flex items-center justify-between rounded-md border px-3 py-2">
              <div>
                <div class="text-xs font-medium">{{ hint.label }}</div>
                <div v-if="hint.description" class="text-[11px] text-muted-foreground">{{ hint.description }}</div>
              </div>
              <UiSwitch
                :model-value="Boolean(form.config[hint.key])"
                :aria-label="hint.label"
                @update:model-value="(v) => (form.config[hint.key] = v)"
              />
            </div>

            <template v-else-if="hint.type === 'string_list'">
              <label class="text-xs font-medium">{{ hint.label }}</label>
              <UiInput
                :model-value="getListConfigString(hint.key)"
                placeholder="Comma-separated values"
                @update:model-value="(v) => setListConfigString(hint.key, v)"
              />
              <p v-if="hint.description" class="text-[11px] text-muted-foreground">{{ hint.description }}</p>
            </template>

            <template v-else-if="hint.type === 'number'">
              <label class="text-xs font-medium">{{ hint.label }}</label>
              <UiInput
                :model-value="form.config[hint.key] ?? ''"
                type="number"
                @update:model-value="(v) => (form.config[hint.key] = v === '' ? '' : Number(v))"
              />
              <p v-if="hint.description" class="text-[11px] text-muted-foreground">{{ hint.description }}</p>
            </template>

            <template v-else>
              <label class="text-xs font-medium">{{ hint.label }}</label>
              <UiInput
                :model-value="form.config[hint.key] ?? ''"
                @update:model-value="(v) => (form.config[hint.key] = v)"
              />
              <p v-if="hint.description" class="text-[11px] text-muted-foreground">{{ hint.description }}</p>
            </template>
          </div>
        </div>
      </div>
    </form>

    <template #footer>
      <UiButton variant="outline" @click="emit('close')">Cancel</UiButton>
      <UiButton type="submit" form="registry-form" :disabled="saving">
        {{ saving ? 'Saving...' : editing ? 'Save Changes' : 'Create Registry' }}
      </UiButton>
    </template>
  </UiDialog>
</template>
