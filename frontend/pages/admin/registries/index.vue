<script setup lang="ts">
import { Plus, ExternalLink, Settings2, Globe, Lock, RefreshCw } from 'lucide-vue-next'
import type { RegistryInstance, RegistryTypeInfo } from '~/types/registry'

definePageMeta({ layout: 'admin' })

const loading = ref(true)
const errorMsg = ref('')
const registries = ref<RegistryInstance[]>([])
const types = ref<RegistryTypeInfo[]>([])
const modalOpen = ref(false)
const editing = ref<RegistryInstance | null>(null)
const togglingId = ref('')

async function loadData() {
  loading.value = true
  errorMsg.value = ''
  try {
    const [regRes, typeRes] = await Promise.all([
      apiFetch<{ registries: RegistryInstance[] }>('/api/admin/registries'),
      apiFetch<{ types: RegistryTypeInfo[] }>('/api/admin/registry-types'),
    ])
    registries.value = regRes.registries || []
    types.value = typeRes.types || []
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to load registries'
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editing.value = null
  modalOpen.value = true
}

function openEdit(reg: RegistryInstance) {
  editing.value = reg
  modalOpen.value = true
}

async function toggleEnabled(reg: RegistryInstance, enabled: boolean) {
  togglingId.value = reg.id
  errorMsg.value = ''
  try {
    const updated = await apiFetch<RegistryInstance>(`/api/admin/registries/${reg.id}/enabled`, {
      method: 'PATCH',
      body: JSON.stringify({ enabled }),
    })
    const idx = registries.value.findIndex((r) => r.id === reg.id)
    if (idx !== -1) registries.value[idx] = updated
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to update registry status'
  } finally {
    togglingId.value = ''
  }
}

const healthVariant = (h: string) => {
  if (h === 'healthy') return 'success'
  if (h === 'degraded') return 'warning'
  if (h === 'unhealthy') return 'destructive'
  return 'secondary'
}

onMounted(loadData)
</script>

<template>
  <div class="space-y-6">
    <div class="flex flex-wrap items-center justify-between gap-4">
      <div>
        <h1 class="text-2xl font-bold tracking-tight">Registries</h1>
        <p class="text-sm text-muted-foreground">
          Create and manage isolated registry instances. Changes take effect live without restarting.
        </p>
      </div>
      <div class="flex items-center gap-2">
        <UiButton variant="outline" size="sm" :disabled="loading" @click="loadData">
          <RefreshCw :class="['h-3.5 w-3.5', loading ? 'animate-spin' : '']" />
          Refresh
        </UiButton>
        <UiButton size="sm" @click="openCreate">
          <Plus class="h-4 w-4" />
          New Registry
        </UiButton>
      </div>
    </div>

    <SharedErrorBanner :message="errorMsg" />

    <div v-if="loading && registries.length === 0" class="space-y-3">
      <div v-for="n in 3" :key="n" class="h-28 rounded-xl border bg-card animate-pulse" />
    </div>

    <SharedEmptyState
      v-else-if="registries.length === 0"
      title="No registries configured"
      description="Create your first registry instance using an installed package ecosystem type."
    >
      <template #action>
        <UiButton @click="openCreate">
          <Plus class="h-4 w-4" />
          Create Registry
        </UiButton>
      </template>
    </SharedEmptyState>

    <div v-else class="grid grid-cols-1 gap-4">
      <UiCard v-for="reg in registries" :key="reg.id" class="p-5">
        <div class="flex flex-col lg:flex-row lg:items-center justify-between gap-4">
          <div class="space-y-2 min-w-0 flex-1">
            <div class="flex flex-wrap items-center gap-2">
              <NuxtLink
                :to="`/admin/registries/${reg.id}`"
                class="text-base font-semibold text-foreground hover:text-primary transition-colors"
              >
                {{ reg.name }}
              </NuxtLink>
              <UiBadge variant="info" class="uppercase font-mono text-[10px]">{{ reg.type }}</UiBadge>
              <UiBadge variant="secondary" class="capitalize">{{ reg.mode }}</UiBadge>
              <UiBadge :variant="healthVariant(reg.health)" class="capitalize">{{ reg.health }}</UiBadge>
              <UiBadge v-if="reg.is_default" variant="success">Default Compat</UiBadge>
              <span class="inline-flex items-center gap-1 text-xs text-muted-foreground">
                <component :is="reg.visibility === 'public' ? Globe : Lock" class="h-3 w-3" />
                {{ reg.visibility }}
              </span>
            </div>

            <p v-if="reg.description" class="text-xs text-muted-foreground">{{ reg.description }}</p>

            <div class="flex flex-wrap items-center gap-4 text-xs text-muted-foreground">
              <span class="font-mono text-slate-700 bg-muted px-2 py-0.5 rounded">{{ reg.base_path }}</span>
              <span v-if="reg.upstream_url">Upstream: <strong class="text-foreground">{{ reg.upstream_url }}</strong></span>
              <span>Artifacts: <strong class="text-foreground">{{ reg.artifact_count }}</strong></span>
              <span>Downloads: <strong class="text-foreground">{{ reg.metrics.downloads }}</strong></span>
            </div>
          </div>

          <div class="flex flex-col sm:flex-row items-start sm:items-center gap-4 lg:gap-6 shrink-0">
            <div class="w-48">
              <SharedStorageBar
                :used-bytes="reg.storage.logical_bytes"
                :quota-bytes="reg.storage_quota_bytes"
                :hosted-bytes="reg.storage.hosted_bytes"
                :cached-bytes="reg.storage.cached_bytes"
              />
            </div>

            <div class="flex items-center gap-2">
              <UiSwitch
                :model-value="reg.enabled"
                :disabled="togglingId === reg.id"
                :aria-label="`Enable ${reg.name}`"
                @update:model-value="(v) => toggleEnabled(reg, v)"
              />

              <NuxtLink :to="`/explore/${reg.slug}`">
                <UiButton variant="outline" size="sm" title="Open Explorer">
                  <ExternalLink class="h-3.5 w-3.5" />
                </UiButton>
              </NuxtLink>

              <UiButton variant="outline" size="sm" title="Quick Edit" @click="openEdit(reg)">
                <Settings2 class="h-3.5 w-3.5" />
              </UiButton>

              <NuxtLink :to="`/admin/registries/${reg.id}`">
                <UiButton size="sm">Manage</UiButton>
              </NuxtLink>
            </div>
          </div>
        </div>
      </UiCard>
    </div>

    <SharedRegistryFormModal
      :open="modalOpen"
      :types="types"
      :editing="editing"
      @close="modalOpen = false"
      @saved="loadData"
    />
  </div>
</template>
