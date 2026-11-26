<script setup lang="ts">
import { RefreshCw } from 'lucide-vue-next'
import type { AuditEntry, RegistryInstance } from '~/types/registry'
import { formatDateTime } from '~/utils/format'

definePageMeta({ layout: 'admin' })

const loading = ref(true)
const errorMsg = ref('')
const entries = ref<AuditEntry[]>([])
const total = ref(0)
const page = ref(1)
const limit = 25
const registryFilter = ref('__all__')
const actionFilter = ref('__all__')
const registries = ref<RegistryInstance[]>([])

const registryOptions = computed(() => [
  { value: '__all__', label: 'All Registries' },
  ...registries.value.map((r) => ({ value: r.id, label: `${r.name} (${r.slug})` })),
])

const AUDIT_ACTIONS = [
  'auth.login',
  'registry.create',
  'registry.update',
  'registry.toggle',
  'registry.delete',
  'token.create',
  'token.revoke',
  'artifact.publish',
  'artifact.delete',
  'cache.purge',
  'settings.update',
  'settings.base_config.apply',
  'user.create',
  'user.delete',
]

const actionOptions = computed(() => [
  { value: '__all__', label: 'All Actions' },
  ...AUDIT_ACTIONS.map((a) => ({ value: a, label: a })),
])

async function loadData() {
  loading.value = true
  errorMsg.value = ''
  try {
    const params = new URLSearchParams({
      limit: String(limit),
      offset: String((page.value - 1) * limit),
    })
    if (registryFilter.value && registryFilter.value !== '__all__') params.set('registry_id', registryFilter.value)
    if (actionFilter.value && actionFilter.value !== '__all__') params.set('action', actionFilter.value)

    const [logRes, regRes] = await Promise.all([
      apiFetch<{ entries: AuditEntry[]; total: number }>(`/api/admin/audit-logs?${params.toString()}`),
      registries.value.length === 0
        ? apiFetch<{ registries: RegistryInstance[] }>('/api/admin/registries')
        : Promise.resolve({ registries: registries.value }),
    ])
    entries.value = logRes.entries || []
    total.value = logRes.total || 0
    registries.value = regRes.registries || []
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to load audit logs'
  } finally {
    loading.value = false
  }
}

onMounted(loadData)
</script>

<template>
  <div class="space-y-6">
    <div class="flex flex-wrap items-center justify-between gap-4">
      <div>
        <h1 class="text-2xl font-bold tracking-tight">Audit Logs</h1>
        <p class="text-sm text-muted-foreground">Security-relevant administrative and publishing events across all registries.</p>
      </div>
      <UiButton variant="outline" size="sm" :disabled="loading" @click="loadData">
        <RefreshCw :class="['h-3.5 w-3.5', loading ? 'animate-spin' : '']" />
        Refresh
      </UiButton>
    </div>

    <div class="flex flex-wrap items-center gap-3">
      <UiSelect
        v-model="registryFilter"
        @update:model-value="() => { page = 1; loadData() }"
      >
        <UiSelectTrigger class="w-56" aria-label="Filter by registry">
          <UiSelectValue placeholder="All Registries" />
        </UiSelectTrigger>
        <UiSelectContent>
          <UiSelectItem v-for="opt in registryOptions" :key="opt.value" :value="opt.value">
            {{ opt.label }}
          </UiSelectItem>
        </UiSelectContent>
      </UiSelect>

      <UiSelect
        v-model="actionFilter"
        @update:model-value="() => { page = 1; loadData() }"
      >
        <UiSelectTrigger class="w-48" aria-label="Filter by action">
          <UiSelectValue placeholder="All Actions" />
        </UiSelectTrigger>
        <UiSelectContent>
          <UiSelectItem v-for="opt in actionOptions" :key="opt.value" :value="opt.value">
            {{ opt.label }}
          </UiSelectItem>
        </UiSelectContent>
      </UiSelect>
    </div>

    <SharedErrorBanner :message="errorMsg" />

    <UiCard class="overflow-hidden">
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="border-b bg-muted/40 text-left text-xs font-medium text-muted-foreground">
              <th class="px-5 py-3">Timestamp</th>
              <th class="px-4 py-3">Actor</th>
              <th class="px-4 py-3">Action</th>
              <th class="px-4 py-3">Target</th>
              <th class="px-4 py-3">Result</th>
            </tr>
          </thead>
          <tbody class="divide-y">
            <tr v-if="loading">
              <td colspan="5" class="px-5 py-8 text-center text-muted-foreground">Loading audit logs...</td>
            </tr>
            <tr v-else-if="entries.length === 0">
              <td colspan="5" class="px-5 py-10 text-center text-muted-foreground">No matching audit entries.</td>
            </tr>
            <tr v-for="entry in entries" :key="entry.id" class="hover:bg-muted/20">
              <td class="px-5 py-3 text-xs text-muted-foreground whitespace-nowrap">
                {{ formatDateTime(entry.timestamp) }}
              </td>
              <td class="px-4 py-3 text-xs">
                <span class="font-medium text-foreground">{{ entry.actor_name }}</span>
                <span class="text-muted-foreground"> ({{ entry.actor_type }})</span>
              </td>
              <td class="px-4 py-3 font-mono text-xs">{{ entry.action }}</td>
              <td class="px-4 py-3 font-mono text-xs text-muted-foreground">{{ entry.target || '—' }}</td>
              <td class="px-4 py-3">
                <UiBadge :variant="entry.success ? 'success' : 'destructive'">
                  {{ entry.success ? 'Success' : 'Failed' }}
                </UiBadge>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </UiCard>

    <SharedPaginationBar
      :page="page"
      :limit="limit"
      :total="total"
      @change="(p) => { page = p; loadData() }"
    />
  </div>
</template>
