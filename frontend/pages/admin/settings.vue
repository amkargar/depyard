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
} from 'lucide-vue-next'
import type { PlatformSettings, UpstreamHealth } from '~/types/registry'

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
