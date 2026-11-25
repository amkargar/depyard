<script setup lang="ts">
import type { Capabilities } from '~/types/registry'

const props = defineProps<{ capabilities: Capabilities }>()

const labels: Array<{ key: keyof Capabilities; label: string }> = [
  { key: 'proxy', label: 'Proxy' },
  { key: 'hosted', label: 'Hosted' },
  { key: 'publish', label: 'Publish' },
  { key: 'search', label: 'Search' },
  { key: 'list_versions', label: 'Versions' },
  { key: 'cache_warm', label: 'Cache Warm' },
  { key: 'metadata_sync', label: 'Metadata Sync' },
  { key: 'artifact_deletion', label: 'Delete' },
  { key: 'version_retraction', label: 'Retract' },
  { key: 'package_ownership', label: 'Ownership' },
  { key: 'signature_verification', label: 'Signatures' },
]

const enabledCaps = computed(() => labels.filter((l) => props.capabilities[l.key]))
</script>

<template>
  <div class="flex flex-wrap gap-1.5">
    <UiBadge v-for="cap in enabledCaps" :key="cap.key" variant="secondary" class="font-normal">
      {{ cap.label }}
    </UiBadge>
  </div>
</template>
