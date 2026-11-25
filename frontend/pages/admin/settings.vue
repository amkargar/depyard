<script setup lang="ts">
import {
  Save,
  Globe,
  Network,
  Clock,
  Activity,
  CheckCircle2,
  XCircle,
  ShieldAlert,
  HardDriveDownload,
  ListChecks,
  RotateCcw,
  Layers,
  RefreshCw,
} from 'lucide-vue-next'
import type {
  PlatformSettings,
  UpstreamHealth,
  BaseConfigImpact,
  BaseConfigRegistryImpact,
} from '~/types/registry'

definePageMeta({ layout: 'admin' })

const loading = ref(true)
const saving = ref(false)
const testing = ref(false)
const errorMsg = ref('')
const savedMsg = ref('')
const testUrl = ref('https://storage.googleapis.com/flutter_infra_release/releases/releases_linux.json')
const testResult = ref<UpstreamHealth | null>(null)
const testError = ref('')

const form = reactive<PlatformSettings>({
  id: 'global',
  site_title: 'Mirror Registry',
  public_base_url: '',
  enable_root_compat_route: true,
  audit_retention_days: 90,
  stats_retention_days: 90,
  upstream_proxy_url: '',
  upstream_timeout_seconds: 30,
  upstream_user_agent: 'MirrorRegistry/1.0',
  upstream_insecure_skip_verify: false,
  default_pub_upstream_url: 'https://pub.dev',
  default_flutter_storage_url: 'https://storage.googleapis.com',
  updated_at: '',
})

async function loadSettings() {
  loading.value = true
  errorMsg.value = ''
  try {
    const res = await apiFetch<PlatformSettings>('/api/admin/settings')
    Object.assign(form, res)
    hydrateFlutterBase(res)
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to load settings'
  } finally {
    loading.value = false
  }
}

async function handleSave() {
  saving.value = true
  errorMsg.value = ''
  savedMsg.value = ''
  try {
    const res = await apiFetch<PlatformSettings>('/api/admin/settings', {
      method: 'PUT',
      body: JSON.stringify({
        site_title: form.site_title.trim(),
        public_base_url: form.public_base_url.trim(),
        enable_root_compat_route: form.enable_root_compat_route,
        audit_retention_days: Math.max(1, Number(form.audit_retention_days) || 90),
        stats_retention_days: Math.max(1, Number(form.stats_retention_days) || 90),
        upstream_proxy_url: form.upstream_proxy_url.trim(),
        upstream_timeout_seconds: Math.max(1, Number(form.upstream_timeout_seconds) || 30),
        upstream_user_agent: form.upstream_user_agent.trim(),
        upstream_insecure_skip_verify: form.upstream_insecure_skip_verify,
        default_pub_upstream_url: form.default_pub_upstream_url.trim(),
        default_flutter_storage_url: form.default_flutter_storage_url.trim(),
        default_flutter_storage_config: { ...flutterBase },
        flutter_base_config_mode: flutterBaseMode.value,
      }),
    })
    Object.assign(form, res)
    savedMsg.value = 'Platform and upstream settings saved and applied live.'
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to save settings'
  } finally {
    saving.value = false
  }
}

async function handleTestUpstream(overrideUrl?: string) {
  const target = (overrideUrl ?? testUrl.value).trim()
  if (!target) return
  testUrl.value = target
  testing.value = true
  testError.value = ''
  testResult.value = null
  try {
    testResult.value = await apiFetch<UpstreamHealth>('/api/admin/upstream/test', {
      method: 'POST',
      body: JSON.stringify({
        url: target,
        proxy_url: form.upstream_proxy_url.trim(),
        insecure_skip_verify: form.upstream_insecure_skip_verify,
        timeout_seconds: Math.min(Math.max(Number(form.upstream_timeout_seconds) || 10, 3), 30),
      }),
    })
  } catch (err: any) {
    testError.value = err?.message || 'Upstream connectivity test failed'
  } finally {
    testing.value = false
  }
}

/* ------------------------------------------------------------------ *
 * Base Flutter Storage settings (platform-wide defaults + inheritance)
 * ------------------------------------------------------------------ */

/** Editable copy of the base Flutter config; persisted with the rest of the settings form. */
const flutterBase = reactive<Record<string, any>>({})
const flutterBaseMode = ref<'defaults' | 'inherit'>('defaults')

const applyOpen = ref(false)
const applyMode = ref<'fill-missing' | 'overwrite'>('fill-missing')
const applyFields = ref<string[]>([])
const applyScope = ref<string[]>([])
const applyPreview = ref<BaseConfigImpact | null>(null)
const applyBusy = ref(false)
const applyError = ref('')
const applyResult = ref<BaseConfigImpact | null>(null)

const BASE_FIELD_LABELS: Record<string, string> = {
  storage_name: 'Storage name',
  releases_base_url: 'Release manifest base URL',
  registry_ttl_seconds: 'Version registry TTL',
  manifest_ttl_seconds: 'Manifest TTL',
  rewrite_manifest_urls: 'Rewrite manifest base_url',
  serve_stale_on_error: 'Serve stale on error',
  upstream_timeout_seconds: 'Upstream timeout',
  max_artifact_size_bytes: 'Max artifact size',
  download_storage: 'Storage categories',
  allowed_prefixes: 'Allowed prefixes',
  upstream_url: 'Default upstream URL',
}

const baseFieldChoices = computed(() => {
  const keys = new Set<string>(Object.keys(flutterBase))
  return [...keys]
    .filter((k) => k !== 'download_storage_mode')
    .map((k) => ({ key: k, label: BASE_FIELD_LABELS[k] || k }))
    .sort((a, b) => a.label.localeCompare(b.label))
})

function hydrateFlutterBase(settings: PlatformSettings) {
  for (const key of Object.keys(flutterBase)) delete flutterBase[key]
  Object.assign(flutterBase, settings.default_flutter_storage_config || {})
  // Preserve the exact "unset" state: an empty map means "no base configured".
  if (!flutterBase.download_storage && Array.isArray(settings.default_flutter_storage_config?.download_storage)) {
    flutterBase.download_storage = settings.default_flutter_storage_config!.download_storage
  }
  flutterBaseMode.value = settings.flutter_base_config_mode === 'inherit' ? 'inherit' : 'defaults'
}

function resetFlutterBase() {
  for (const key of Object.keys(flutterBase)) delete flutterBase[key]
  flutterBaseMode.value = 'defaults'
  savedMsg.value = ''
}

async function previewApply() {
  applyBusy.value = true
  applyError.value = ''
  applyResult.value = null
  try {
    applyPreview.value = await apiFetch<BaseConfigImpact>(
      '/api/admin/settings/base-config/flutter/impact',
    )
  } catch (err: any) {
    applyError.value = err?.message || 'Failed to preview the base settings impact'
  } finally {
    applyBusy.value = false
  }
}

async function runApply(dryRun: boolean) {
  applyBusy.value = true
  applyError.value = ''
  try {
    const res = await apiFetch<BaseConfigImpact>('/api/admin/settings/base-config/flutter/apply', {
      method: 'POST',
      body: JSON.stringify({
        mode: applyMode.value,
        fields: applyFields.value,
        registry_ids: applyScope.value,
        dry_run: dryRun,
      }),
    })
    if (dryRun) {
      applyPreview.value = res
    } else {
      applyResult.value = res
      applyPreview.value = res
      savedMsg.value = `Applied base Flutter Storage settings to ${res.applied} registr${res.applied === 1 ? 'y' : 'ies'}${
        res.failed ? ` (${res.failed} failed)` : ''
      }.`
    }
  } catch (err: any) {
    applyError.value = err?.message || 'Failed to apply base settings'
  } finally {
    applyBusy.value = false
  }
}

function openApplyDialog() {
  applyOpen.value = true
  applyMode.value = 'fill-missing'
  applyFields.value = []
  applyScope.value = []
  applyPreview.value = null
  applyResult.value = null
  applyError.value = ''
  void previewApply()
}

function toggleApplyField(key: string) {
  applyFields.value = applyFields.value.includes(key)
    ? applyFields.value.filter((k) => k !== key)
    : [...applyFields.value, key]
}

function changeCountFor(reg: BaseConfigRegistryImpact): number {
  return reg.changes?.length ?? 0
}

const applyTotals = computed(() => {
  const regs = applyPreview.value?.registries || []
  const changed = regs.filter((r) => changeCountFor(r) > 0).length
  const fields = regs.reduce((n, r) => n + changeCountFor(r), 0)
  return { registries: regs.length, changed, fields }
})

function formatApplyValue(v: unknown): string {
  if (v === undefined || v === null || v === '') return '—'
  if (typeof v === 'boolean') return v ? 'true' : 'false'
  if (Array.isArray(v)) {
    if (v.length && typeof v[0] === 'object') {
      return v.map((c: any) => c.name).filter(Boolean).join(', ') || `${v.length} entries`
    }
    return v.join(', ')
  }
  if (typeof v === 'object') return JSON.stringify(v)
  return String(v)
}

onMounted(loadSettings)
</script>

<template>
  <div class="space-y-6 max-w-4xl">
    <div class="flex flex-wrap items-start justify-between gap-4">
      <div>
        <h1 class="text-2xl font-bold tracking-tight">Platform &amp; Upstream Settings</h1>
        <p class="text-sm text-muted-foreground">
          Configure global routing compatibility, outbound upstream proxy &amp; TLS defaults, default ecosystem mirrors, and retention policies. Changes apply live without restarting the server.
        </p>
      </div>
    </div>

    <SharedErrorBanner :message="errorMsg" />
    <div
      v-if="savedMsg"
      class="rounded-lg border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-emerald-800"
    >
      {{ savedMsg }}
    </div>

    <form class="space-y-6" @submit.prevent="handleSave">
      <!-- General & Routing Compatibility -->
      <UiCard class="p-6 space-y-5">
        <div class="flex items-center gap-2 border-b pb-3">
          <Globe class="h-4 w-4 text-primary" />
          <div>
            <h2 class="text-sm font-semibold">General &amp; Routing Compatibility</h2>
            <p class="text-xs text-muted-foreground">
              Controls public branding, external URL rewriting for Pub &amp; Flutter manifests, and root-level compatibility endpoints.
            </p>
          </div>
        </div>

        <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div class="space-y-1.5">
            <label class="text-xs font-medium">Site Title</label>
            <UiInput v-model="form.site_title" required />
          </div>

          <div class="space-y-1.5">
            <label class="text-xs font-medium">Public Base URL (optional override)</label>
            <UiInput v-model="form.public_base_url" placeholder="https://registry.example.com" />
            <p class="text-[11px] text-muted-foreground">
              Used for rewritten Pub <code class="font-mono">archive_url</code> and Flutter <code class="font-mono">base_url</code>. Leave empty to infer from Host/X-Forwarded-Proto.
            </p>
          </div>
        </div>

        <div class="flex items-center justify-between gap-4 rounded-lg border bg-muted/20 p-4">
          <div>
            <div class="text-sm font-medium">Enable Root-Level Compatibility Routes</div>
            <p class="text-xs text-muted-foreground mt-0.5">
              Routes single-registry root URLs directly to each ecosystem's default instance:
              Pub (<code class="font-mono">/api/packages/*</code>, <code class="font-mono">/packages/*</code>) and
              Flutter Storage (<code class="font-mono">/flutter_infra_release/*</code>, <code class="font-mono">/download.flutter.io/*</code>, <code class="font-mono">/dart-archive/*</code>).
            </p>
          </div>
          <UiSwitch
            v-model="form.enable_root_compat_route"
            aria-label="Enable root-level compatibility routes"
          />
        </div>
      </UiCard>

      <!-- Global Upstream Networking & Proxy -->
      <UiCard class="p-6 space-y-5">
        <div class="flex items-center gap-2 border-b pb-3">
          <Network class="h-4 w-4 text-primary" />
          <div>
            <h2 class="text-sm font-semibold">Global Upstream Networking &amp; Proxy</h2>
            <p class="text-xs text-muted-foreground">
              Default outbound HTTP/SOCKS5 proxy, timeouts, User-Agent, and TLS verification applied to all proxy and hybrid registries unless overridden per registry.
            </p>
          </div>
        </div>

        <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div class="space-y-1.5 sm:col-span-2">
            <label class="text-xs font-medium">Outbound Upstream Proxy URL (optional)</label>
            <UiInput
              v-model="form.upstream_proxy_url"
              placeholder="http://proxy.internal:8080 or socks5://127.0.0.1:1080"
            />
            <p class="text-[11px] text-muted-foreground">
              Supports <code class="font-mono">http://</code>, <code class="font-mono">https://</code>, <code class="font-mono">socks5://</code>, and <code class="font-mono">socks5h://</code>. Leave empty to connect directly (or honor container environment proxy variables).
            </p>
          </div>

          <div class="space-y-1.5">
            <label class="text-xs font-medium">Default Upstream Timeout (seconds)</label>
            <UiInput
              v-model="form.upstream_timeout_seconds"
              type="number"
              min="1"
              max="3600"
              required
            />
          </div>

          <div class="space-y-1.5">
            <label class="text-xs font-medium">Outbound User-Agent</label>
            <UiInput v-model="form.upstream_user_agent" placeholder="MirrorRegistry/1.0" />
          </div>
        </div>

        <div class="flex items-center justify-between gap-4 rounded-lg border border-amber-200/70 bg-amber-50/40 p-4">
          <div class="flex items-start gap-2.5">
            <ShieldAlert class="h-4 w-4 text-amber-600 mt-0.5 shrink-0" />
            <div>
              <div class="text-sm font-medium">Skip TLS Certificate Verification (Global Default)</div>
              <p class="text-xs text-muted-foreground mt-0.5">
                Only enable if mirroring internal upstream servers with self-signed certificates. You can also enable this selectively on individual registries.
              </p>
            </div>
          </div>
          <UiSwitch
            v-model="form.upstream_insecure_skip_verify"
            aria-label="Skip TLS certificate verification globally"
          />
        </div>

        <div class="grid grid-cols-1 sm:grid-cols-2 gap-4 pt-2 border-t">
          <div class="space-y-1.5">
            <label class="text-xs font-medium">Default Pub Upstream URL</label>
            <UiInput
              v-model="form.default_pub_upstream_url"
              type="url"
              placeholder="https://pub.dev"
            />
            <p class="text-[11px] text-muted-foreground">
              Pre-filled when creating new Pub mirrors (e.g. <code class="font-mono">https://pub.dev</code>).
            </p>
          </div>

          <div class="space-y-1.5">
            <label class="text-xs font-medium">Default Flutter Storage Upstream URL</label>
            <UiInput
              v-model="form.default_flutter_storage_url"
              type="url"
              placeholder="https://storage.googleapis.com"
            />
            <p class="text-[11px] text-muted-foreground">
              Pre-filled when creating new Flutter Storage mirrors (e.g. <code class="font-mono">https://storage.googleapis.com</code> or a regional mirror).
            </p>
          </div>
        </div>
      </UiCard>

      <!-- Base Flutter Storage settings -->
      <UiCard class="p-6 space-y-4">
        <div class="flex flex-wrap items-start justify-between gap-3 border-b pb-3">
          <div class="flex items-center gap-2">
            <HardDriveDownload class="h-4 w-4 text-primary" />
            <div>
              <h2 class="text-sm font-semibold">Base Flutter Storage Settings</h2>
              <p class="text-xs text-muted-foreground">
                Platform-wide defaults for every Flutter Storage registry. Used to seed new registries, to push
                into existing ones, and — in inheritance mode — for any field a registry leaves unset.
              </p>
            </div>
          </div>
          <div class="flex items-center gap-2">
            <UiButton
              type="button"
              variant="outline"
              size="sm"
              :disabled="applyBusy"
              @click="openApplyDialog"
            >
              <ListChecks class="h-4 w-4" />
              Apply to existing…
            </UiButton>
            <UiButton type="button" variant="ghost" size="sm" @click="resetFlutterBase">
              <RotateCcw class="h-4 w-4" />
              Reset
            </UiButton>
          </div>
        </div>

        <div class="flex items-center justify-between gap-4 rounded-lg border border-sky-200/70 bg-sky-50/40 p-4">
          <div class="flex items-start gap-2.5">
            <Layers class="h-4 w-4 text-sky-600 mt-0.5 shrink-0" />
            <div>
              <div class="text-sm font-medium">Inherit base settings for unset fields</div>
              <p class="text-xs text-muted-foreground mt-0.5">
                <template v-if="flutterBaseMode === 'inherit'">
                  Registries that leave a field unset now follow the base <strong>live</strong> — changing a value
                  here updates every inheriting registry within seconds, without touching stored configs.
                </template>
                <template v-else>
                  Base settings only seed new registries and are applied explicitly. Existing registries keep
                  their stored values until you push the base into them.
                </template>
              </p>
            </div>
          </div>
          <UiSwitch
            :model-value="flutterBaseMode === 'inherit'"
            aria-label="Inherit base Flutter Storage settings for unset fields"
            @update:model-value="(v: boolean) => (flutterBaseMode = v ? 'inherit' : 'defaults')"
          />
        </div>

        <FlutterStorageSettings
          :config="flutterBase"
          :upstream-url="form.default_flutter_storage_url"
          mode="base"
          @update:config="(next: Record<string, any>) => Object.assign(flutterBase, next)"
        />

        <p class="text-[11px] text-muted-foreground border-t pt-3">
          Stored in <code class="font-mono">default_flutter_storage_config</code>. Validated with the same strict
          parser the runtime uses, so unknown keys, bad URLs, and out-of-range values are rejected on save.
          {{ baseFieldChoices.length }} field{{ baseFieldChoices.length === 1 ? '' : 's' }} configurable.
        </p>
      </UiCard>

      <!-- Retention -->
      <UiCard class="p-6 space-y-4">
        <div class="flex items-center gap-2 border-b pb-3">
          <Clock class="h-4 w-4 text-primary" />
          <div>
            <h2 class="text-sm font-semibold">Data Retention</h2>
            <p class="text-xs text-muted-foreground">Control how long audit logs and usage statistics buckets are retained.</p>
          </div>
        </div>

        <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div class="space-y-1.5">
            <label class="text-xs font-medium">Audit Log Retention (days)</label>
            <UiInput v-model="form.audit_retention_days" type="number" min="1" />
          </div>
          <div class="space-y-1.5">
            <label class="text-xs font-medium">Statistics Retention (days)</label>
            <UiInput v-model="form.stats_retention_days" type="number" min="1" />
          </div>
        </div>
      </UiCard>

      <div class="flex items-center gap-3">
        <UiButton type="submit" :disabled="saving || loading">
          <Save class="h-4 w-4" />
          {{ saving ? 'Saving...' : 'Save All Settings' }}
        </UiButton>
      </div>
    </form>

    <!-- Apply base settings to existing registries -->
    <UiDialog
      :open="applyOpen"
      title="Apply base Flutter Storage settings"
      description="Preview exactly which fields change per registry before writing anything."
      max-width-class="max-w-3xl"
      @close="applyOpen = false"
    >
      <div class="space-y-4">
        <SharedErrorBanner :message="applyError" />

        <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div class="space-y-1.5">
            <label class="text-xs font-medium">Apply mode</label>
            <UiSelect v-model="applyMode" @change="runApply(true)">
              <option value="fill-missing">Fill missing only — never overwrite registry values</option>
              <option value="overwrite">Overwrite — replace selected fields with base values</option>
            </UiSelect>
            <p class="text-[11px] text-muted-foreground">
              {{
                applyMode === 'fill-missing'
                  ? 'Safe default: only keys the registry does not set are written. Storage categories are merged per category.'
                  : 'Destructive: the selected fields are replaced. Review the diff below before confirming.'
              }}
            </p>
          </div>
          <div class="space-y-1.5">
            <label class="text-xs font-medium">Limit to fields (optional)</label>
            <div class="rounded-md border bg-background px-3 py-2 max-h-32 overflow-y-auto space-y-1">
              <label
                v-for="choice in baseFieldChoices"
                :key="choice.key"
                class="flex items-center gap-2 text-xs cursor-pointer"
              >
                <input
                  type="checkbox"
                  class="h-3.5 w-3.5 rounded border-input"
                  :checked="applyFields.includes(choice.key)"
                  @change="toggleApplyField(choice.key)"
                />
                <span>{{ choice.label }}</span>
              </label>
              <p v-if="!baseFieldChoices.length" class="text-[11px] text-muted-foreground">
                No base fields configured yet — save the base settings first.
              </p>
            </div>
            <p class="text-[11px] text-muted-foreground">Nothing selected = every field present in the base.</p>
          </div>
        </div>

        <div class="rounded-lg border">
          <div class="flex flex-wrap items-center justify-between gap-2 border-b bg-muted/30 px-3 py-2">
            <div class="text-xs font-medium">
              Impact preview —
              {{ applyTotals.changed }} of {{ applyTotals.registries }} registr{{ applyTotals.registries === 1 ? 'y' : 'ies' }}
              affected, {{ applyTotals.fields }} field{{ applyTotals.fields === 1 ? '' : 's' }} to write
            </div>
            <UiButton type="button" variant="ghost" size="sm" :disabled="applyBusy" @click="runApply(true)">
              <RefreshCw class="h-3.5 w-3.5" :class="{ 'animate-spin': applyBusy }" />
              Refresh preview
            </UiButton>
          </div>

          <div class="max-h-72 overflow-y-auto divide-y">
            <div v-if="applyBusy && !applyPreview" class="p-6 text-center text-xs text-muted-foreground">
              Computing impact…
            </div>
            <div
              v-else-if="!applyPreview?.registries?.length"
              class="p-6 text-center text-xs text-muted-foreground"
            >
              No Flutter Storage registries found.
            </div>
            <div v-for="reg in applyPreview?.registries || []" :key="reg.id" class="p-3 space-y-1.5">
              <div class="flex flex-wrap items-center justify-between gap-2">
                <div class="text-xs font-medium">
                  {{ reg.name }}
                  <code class="ml-1 font-mono text-[11px] text-muted-foreground">/r/{{ reg.slug }}</code>
                </div>
                <UiBadge
                  :variant="reg.error ? 'destructive' : changeCountFor(reg) ? 'default' : 'secondary'"
                  class="text-[10px]"
                >
                  {{
                    reg.error
                      ? 'error'
                      : changeCountFor(reg)
                        ? `${changeCountFor(reg)} change${changeCountFor(reg) === 1 ? '' : 's'}`
                        : 'in sync'
                  }}
                </UiBadge>
              </div>
              <p v-if="reg.error" class="text-[11px] text-destructive">{{ reg.error }}</p>
              <div v-if="changeCountFor(reg)" class="space-y-1">
                <div
                  v-for="change in reg.changes"
                  :key="change.key"
                  class="grid grid-cols-1 sm:grid-cols-[140px_1fr] gap-1 rounded-md bg-muted/30 px-2 py-1 text-[11px]"
                >
                  <span class="font-medium">{{ BASE_FIELD_LABELS[change.key] || change.key }}</span>
                  <span class="font-mono break-all">
                    <span class="text-muted-foreground">{{ formatApplyValue(change.from) }}</span>
                    <span class="mx-1 text-muted-foreground">→</span>
                    <span class="text-foreground">{{ formatApplyValue(change.to) }}</span>
                  </span>
                </div>
              </div>
            </div>
          </div>
        </div>

        <div
          v-if="applyMode === 'overwrite'"
          class="rounded-lg border border-amber-200/70 bg-amber-50/40 px-3 py-2 text-[11px] text-amber-900"
        >
          Overwrite mode replaces stored registry values. Cached artifacts are never deleted.
        </div>
      </div>

      <template #footer>
        <div class="flex items-center justify-end gap-2 w-full">
          <UiButton type="button" variant="ghost" @click="applyOpen = false">Close</UiButton>
          <UiButton
            type="button"
            :variant="applyMode === 'overwrite' ? 'destructive' : 'default'"
            :disabled="applyBusy || applyTotals.changed === 0"
            @click="runApply(false)"
          >
            {{ applyBusy ? 'Applying…' : `Apply to ${applyTotals.changed} registr${applyTotals.changed === 1 ? 'y' : 'ies'}` }}
          </UiButton>
        </div>
      </template>
    </UiDialog>

    <!-- Live Upstream Connectivity Tester -->
    <UiCard class="p-6 space-y-4">
      <div class="flex items-center gap-2 border-b pb-3">
        <Activity class="h-4 w-4 text-primary" />
        <div>
          <h2 class="text-sm font-semibold">Live Upstream Connectivity Test</h2>
          <p class="text-xs text-muted-foreground">
            Verify outbound reachability using the proxy URL, timeout, and TLS settings entered above (even before saving).
          </p>
        </div>
      </div>

      <div class="flex flex-wrap items-center gap-2">
        <UiInput
          v-model="testUrl"
          type="url"
          class="flex-1 min-w-[260px]"
          placeholder="https://storage.googleapis.com/flutter_infra_release/releases/releases_linux.json"
        />
        <UiButton type="button" variant="outline" :disabled="testing" @click="handleTestUpstream()">
          <Activity class="h-4 w-4" />
          {{ testing ? 'Testing...' : 'Test Connection' }}
        </UiButton>
      </div>

      <div class="flex flex-wrap items-center gap-2 text-xs">
        <span class="text-muted-foreground">Quick presets:</span>
        <button
          type="button"
          class="rounded border px-2 py-0.5 hover:bg-muted transition-colors"
          @click="handleTestUpstream(form.default_pub_upstream_url || 'https://pub.dev')"
        >
          Pub Upstream
        </button>
        <button
          type="button"
          class="rounded border px-2 py-0.5 hover:bg-muted transition-colors"
          @click="
            handleTestUpstream(
              `${(form.default_flutter_storage_url || 'https://storage.googleapis.com').replace(/\/$/, '')}/flutter_infra_release/releases/releases_linux.json`,
            )
          "
        >
          Flutter Releases Manifest
        </button>
      </div>

      <SharedErrorBanner :message="testError" />

      <div
        v-if="testResult"
        :class="[
          'flex items-center justify-between gap-3 rounded-lg border px-4 py-3 text-xs',
          testResult.available
            ? 'border-emerald-200 bg-emerald-50 text-emerald-900'
            : 'border-red-200 bg-red-50 text-red-900',
        ]"
      >
        <div class="flex items-center gap-2">
          <CheckCircle2 v-if="testResult.available" class="h-4 w-4 text-emerald-600 shrink-0" />
          <XCircle v-else class="h-4 w-4 text-red-600 shrink-0" />
          <div>
            <span class="font-semibold">
              {{ testResult.available ? 'Reachable' : 'Connection Failed' }}
            </span>
            <span v-if="testResult.status_code"> · HTTP {{ testResult.status_code }}</span>
            <span> · {{ testResult.latency_ms }} ms</span>
            <div v-if="testResult.error" class="mt-0.5 opacity-90">{{ testResult.error }}</div>
          </div>
        </div>
      </div>
    </UiCard>
  </div>
</template>
