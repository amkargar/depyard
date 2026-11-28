<script setup lang="ts">
import type { PublicRegistryView } from '~/types/registry'
const props = defineProps<{ registry: PublicRegistryView }>()
const base = computed(() => `${window.location.origin}${props.registry.base_path}`)
const endpoint = computed(() => {
  switch (props.registry.type) {
    case 'cargo': return `sparse+${base.value}/`
    case 'nuget': return `${base.value}/v3/index.json`
    default: return base.value
  }
})
</script>

<template>
  <main class="mx-auto max-w-3xl space-y-5 px-4 py-12 sm:px-6">
    <NuxtLink to="/" class="text-sm text-primary">← Registry directory</NuxtLink>
    <section class="space-y-4 rounded-xl border bg-card p-6">
      <div class="flex items-center gap-2">
        <UiBadge variant="info">{{ registry.type }}</UiBadge>
        <UiBadge variant="secondary">{{ registry.mode }}</UiBadge>
      </div>
      <h1 class="text-2xl font-semibold">{{ registry.name }}</h1>
      <p v-if="registry.description" class="text-muted-foreground">{{ registry.description }}</p>
      <h2 class="font-medium">Registry endpoint</h2>
      <code class="block break-all rounded bg-muted p-3">{{ endpoint }}</code>
      <p v-if="registry.type === 'oci'" class="text-sm text-muted-foreground">
        Docker uses the registry hostname, not the slug URL. Use a dedicated hostname or enable root routing for the default OCI instance.
      </p>
      <p class="text-sm text-muted-foreground">
        {{ registry.artifact_count }} stored objects · {{ registry.downloads }} downloads
      </p>
    </section>
  </main>
</template>
