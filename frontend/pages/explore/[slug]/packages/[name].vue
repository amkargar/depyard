<script setup lang="ts">
import type { PublicRegistryView } from '~/types/registry'
import { getRegistryExplorer } from '~/utils/registryExplorers'

const route = useRoute()
const slug = computed(() => String(route.params.slug || ''))
const packageName = computed(() => String(route.params.name || ''))

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
      <div class="h-44 rounded-xl border bg-card animate-pulse" />
    </div>

    <div v-else-if="errorMsg" class="mx-auto max-w-3xl px-4 sm:px-6 py-12 space-y-4">
      <SharedErrorBanner :message="errorMsg" />
      <NuxtLink :to="`/explore/${slug}`">
        <UiButton variant="outline">Back to Registry</UiButton>
      </NuxtLink>
    </div>

    <component
      :is="explorerMapping.detail"
      v-else-if="registry && explorerMapping"
      :registry="registry"
      :package-name="packageName"
    />

    <div v-else-if="registry" class="mx-auto max-w-3xl px-4 sm:px-6 py-12">
      <SharedEmptyState
        :title="`No package detail UI registered for '${registry.type}'`"
        description="Add a dedicated detail view component in utils/registryExplorers.ts."
      />
    </div>
  </div>
</template>
