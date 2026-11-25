<script setup lang="ts">
import { Save } from 'lucide-vue-next'
import type { PlatformSettings } from '~/types/registry'

definePageMeta({ layout: 'admin' })

const loading = ref(true)
const saving = ref(false)
const errorMsg = ref('')
const savedMsg = ref('')

const form = reactive<PlatformSettings>({
  id: 'global',
  site_title: 'Mirror Registry',
  public_base_url: '',
  enable_root_compat_route: true,
  audit_retention_days: 90,
  stats_retention_days: 90,
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
        site_title: form.site_title,
        public_base_url: form.public_base_url,
        enable_root_compat_route: form.enable_root_compat_route,
        audit_retention_days: Number(form.audit_retention_days) || 90,
        stats_retention_days: Number(form.stats_retention_days) || 90,
      }),
    })
    Object.assign(form, res)
    savedMsg.value = 'Platform settings saved.'
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to save settings'
  } finally {
    saving.value = false
  }
}

onMounted(loadSettings)
</script>

<template>
  <div class="space-y-6 max-w-2xl">
    <div>
      <h1 class="text-2xl font-bold tracking-tight">Platform Settings</h1>
      <p class="text-sm text-muted-foreground">
        Configure global routing compatibility, external base URL, and retention settings.
      </p>
    </div>

    <SharedErrorBanner :message="errorMsg" />
    <div
      v-if="savedMsg"
      class="rounded-lg border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-emerald-800"
    >
      {{ savedMsg }}
    </div>

    <UiCard class="p-6">
      <form class="space-y-5" @submit.prevent="handleSave">
        <div class="space-y-1.5">
          <label class="text-xs font-medium">Site Title</label>
          <UiInput v-model="form.site_title" required />
        </div>

        <div class="space-y-1.5">
          <label class="text-xs font-medium">Public Base URL (optional override)</label>
          <UiInput v-model="form.public_base_url" placeholder="https://registry.example.com" />
          <p class="text-[11px] text-muted-foreground">
            Leave empty to infer automatically from incoming Host and X-Forwarded-Proto headers.
          </p>
        </div>

        <div class="flex items-center justify-between rounded-lg border bg-muted/20 p-4">
          <div>
            <div class="text-sm font-medium">Enable Root-Level Compatibility Route</div>
            <p class="text-xs text-muted-foreground mt-0.5">
              Routes single-registry root protocol URLs (such as <code class="font-mono">/api/packages/*</code>) directly to the default registry instance for seamless backward compatibility.
            </p>
          </div>
          <UiSwitch
            v-model="form.enable_root_compat_route"
            aria-label="Enable root-level compatibility route"
          />
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

        <div class="pt-2">
          <UiButton type="submit" :disabled="saving || loading">
            <Save class="h-4 w-4" />
            {{ saving ? 'Saving...' : 'Save Settings' }}
          </UiButton>
        </div>
      </form>
    </UiCard>
  </div>
</template>
