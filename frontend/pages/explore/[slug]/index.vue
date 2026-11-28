<script setup lang="ts">
import type { PublicRegistryView } from '~/types/registry'
import { getRegistryExplorer } from '~/utils/registryExplorers'

const route = useRoute()
const slug = computed(() => String(route.params.slug || ''))

const loading = ref(true)
const errorMsg = ref('')
const registry = ref<PublicRegistryView | null>(null)

const explorerMapping = computed(() =>
  registry.value ? getRegistryExplorer(registry.value.type) : null,
)

async function loadRegistry() {
  loading.value = true
  errorMsg.value = ''
  try {
    registry.value = await apiFetch<PublicRegistryView>(
      `/api/public/registries/${encodeURIComponent(slug.value)}`,
    )
  } catch (err: any) {
    errorMsg.value = err?.message || 'Unable to load registry'
    registry.value = null
  } finally {
    loading.value = false
  }
}

onMounted(loadRegistry)
watch(slug, loadRegistry)
</script>

<template>
  <div>
    <div v-if="loading" class="mx-auto max-w-6xl px-4 sm:px-6 py-12">
      <div class="h-40 rounded-xl border bg-card animate-pulse" />
    </div>

    <div v-else-if="errorMsg" class="mx-auto max-w-3xl px-4 sm:px-6 py-12 space-y-4">
      <SharedErrorBanner :message="errorMsg" />
      <NuxtLink to="/">
        <UiButton variant="outline">Back to Registry Directory</UiButton>
      </NuxtLink>
    </div>

    <component
      :is="explorerMapping.explorer"
      v-else-if="registry && explorerMapping"
      :registry="registry"
    />

    <SharedRepositoryConnection v-else-if="registry" :registry="registry" />
  </div>
</template>
