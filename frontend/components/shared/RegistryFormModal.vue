<script setup lang="ts">
import type {
  RegistryInstance,
  RegistryTypeInfo,
  RepositoryMode,
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

const saving = ref(false)
const errorMsg = ref('')

const form = reactive({
  name: '',
  slug: '',
  type: '',
  description: '',
  enabled: true,
  mode: 'hybrid' as RepositoryMode,
  upstream_url: 'https://pub.dev',
  visibility: 'public' as Visibility,
  allow_anonymous_read: true,
  require_auth_publish: true,
  quota_mb: 0,
  metadata_ttl_seconds: 300,
  archive_ttl_seconds: 0,
  cache_max_mb: 0,
  is_default: false,
  hostname: '',
  config: {} as Record<string, any>,
})

const selectedType = computed(() =>
  props.types.find((t) => t.type === form.type) ?? props.types[0],
)

const supportedModes = computed<RepositoryMode[]>(() =>
  selectedType.value?.supported_modes ?? ['proxy', 'hosted', 'hybrid'],
)

const needsUpstream = computed(() => form.mode === 'proxy' || form.mode === 'hybrid')

watch(
  () => [props.open, props.editing, props.types],
  () => {
    if (!props.open) return
    errorMsg.value = ''
    if (props.editing) {
      form.name = props.editing.name
      form.slug = props.editing.slug
      form.type = props.editing.type
      form.description = props.editing.description || ''
      form.enabled = props.editing.enabled
      form.mode = props.editing.mode
      form.upstream_url = props.editing.upstream_url || ''
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
      form.upstream_url = form.type === 'pub' ? 'https://pub.dev' : ''
      form.visibility = 'public'
      form.allow_anonymous_read = true
      form.require_auth_publish = true
      form.quota_mb = 0
      form.metadata_ttl_seconds = 300
      form.archive_ttl_seconds = 0
      form.cache_max_mb = 0
      form.is_default = false
      form.hostname = ''
      form.config = {}
      if (defType?.config_hints) {
        for (const h of defType.config_hints) {
          if (h.default_value !== undefined) {
            form.config[h.key] = h.default_value
          }
        }
      }
    }
  },
  { immediate: true },
)

watch(
  () => form.type,
  () => {
    if (props.editing) return
    const modes = supportedModes.value
    if (!modes.includes(form.mode) && modes.length > 0) {
      form.mode = modes[0]
    }
  },
)

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
        if (h.type === 'number') {
          cleanConfig[h.key] = Number(val)
        } else {
          cleanConfig[h.key] = val
        }
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
        eviction_policy: 'lru',
        negative_cache_ttl_seconds: 60,
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
    description="Instances activate immediately without restarting the server."
    max-width-class="max-w-2xl"
    @close="emit('close')"
  >
    <form id="registry-form" class="space-y-5" @submit.prevent="handleSubmit">
      <SharedErrorBanner :message="errorMsg" />

      <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
        <div class="space-y-1.5">
          <label class="text-xs font-medium text-foreground">Registry Type</label>
          <UiSelect v-model="form.type" :disabled="Boolean(editing)">
            <option v-for="t in types" :key="t.type" :value="t.type">
              {{ t.display_name }} ({{ t.type }})
            </option>
          </UiSelect>
          <p class="text-[11px] text-muted-foreground">
            Only registry types compiled into this server are selectable.
          </p>
        </div>

        <div class="space-y-1.5">
          <label class="text-xs font-medium text-foreground">Repository Mode</label>
          <UiSelect v-model="form.mode">
            <option v-for="m in supportedModes" :key="m" :value="m">
              {{ m === 'proxy' ? 'Proxy (Upstream Cache)' : m === 'hosted' ? 'Hosted (Local Publish)' : 'Hybrid (Hosted + Upstream Proxy)' }}
            </option>
          </UiSelect>
        </div>
      </div>

      <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
        <div class="space-y-1.5">
          <label class="text-xs font-medium text-foreground">Display Name</label>
          <UiInput
            v-model="form.name"
            placeholder="e.g. Pub Team Mobile"
            required
            @blur="slugifyName"
          />
        </div>
        <div class="space-y-1.5">
          <label class="text-xs font-medium text-foreground">Slug (URL path)</label>
          <UiInput
            v-model="form.slug"
            placeholder="e.g. pub-team-mobile"
            :disabled="Boolean(editing)"
            required
          />
          <p class="text-[11px] text-muted-foreground font-mono">
            Base URL: /r/{{ form.slug || 'your-slug' }}
          </p>
        </div>
      </div>

      <div class="space-y-1.5">
        <label class="text-xs font-medium text-foreground">Description</label>
        <UiInput v-model="form.description" placeholder="Short description of what this repository hosts or mirrors" />
      </div>

      <div v-if="needsUpstream" class="space-y-1.5">
        <label class="text-xs font-medium text-foreground">Upstream URL</label>
        <UiInput
          v-model="form.upstream_url"
          type="url"
          placeholder="https://pub.dev"
          required
        />
      </div>

      <div class="grid grid-cols-1 sm:grid-cols-2 gap-4 pt-1">
        <div class="space-y-1.5">
          <label class="text-xs font-medium text-foreground">Visibility</label>
          <UiSelect v-model="form.visibility">
            <option value="public">Public</option>
            <option value="private">Private (Token / Auth Required)</option>
          </UiSelect>
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
            <div class="text-xs font-medium">Default Compatibility Instance</div>
            <div class="text-[11px] text-muted-foreground">Serves optional root compat routes</div>
          </div>
          <UiSwitch v-model="form.is_default" aria-label="Default compatibility instance" />
        </div>
      </div>

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
        </div>
      </div>

      <!-- Ecosystem-specific validated settings rendered from TypeInfo hints -->
      <div v-if="selectedType?.config_hints?.length" class="space-y-3 border-t pt-4">
        <h3 class="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          {{ selectedType.display_name }} Settings
        </h3>
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3.5">
          <div v-for="hint in selectedType.config_hints" :key="hint.key" class="space-y-1">
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
                placeholder="Comma-separated patterns (e.g. acme_*, internal_*)"
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
