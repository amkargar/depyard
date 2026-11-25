<script setup lang="ts">
import { Trash2, ExternalLink } from 'lucide-vue-next'
import type { ArtifactDescriptor } from '~/types/registry'
import { formatBytes, formatRelative } from '~/utils/format'

defineProps<{
  items: ArtifactDescriptor[]
  registrySlug: string
  canDelete: boolean
  loading?: boolean
}>()

const emit = defineEmits<{
  (e: 'delete', artifact: ArtifactDescriptor): void
}>()
</script>

<template>
  <div class="overflow-x-auto rounded-lg border bg-card">
    <table class="w-full text-sm">
      <thead>
        <tr class="border-b bg-muted/40 text-left text-xs font-medium text-muted-foreground">
          <th class="px-4 py-3">Artifact</th>
          <th class="px-4 py-3">Latest</th>
          <th class="px-4 py-3">Origin</th>
          <th class="px-4 py-3">Size</th>
          <th class="px-4 py-3">Digest</th>
          <th class="px-4 py-3">Updated</th>
          <th class="px-4 py-3 text-right">Actions</th>
        </tr>
      </thead>
      <tbody class="divide-y">
        <tr v-if="loading">
          <td colspan="7" class="px-4 py-8 text-center text-muted-foreground">Loading artifacts...</td>
        </tr>
        <tr v-else-if="items.length === 0">
          <td colspan="7" class="px-4 py-10 text-center text-muted-foreground">
            No artifacts found in this registry.
          </td>
        </tr>
        <tr v-for="item in items" :key="item.id" class="hover:bg-muted/20 transition-colors">
          <td class="px-4 py-3">
            <div class="font-medium text-foreground">{{ item.name }}</div>
            <div v-if="item.description" class="text-xs text-muted-foreground line-clamp-1 max-w-xs">
              {{ item.description }}
            </div>
          </td>
          <td class="px-4 py-3 font-mono text-xs">
            {{ item.version || '—' }}
            <span v-if="item.version_count" class="text-muted-foreground">
              ({{ item.version_count }})
            </span>
          </td>
          <td class="px-4 py-3">
            <UiBadge :variant="item.origin === 'hosted' ? 'info' : 'secondary'">
              {{ item.origin }}
            </UiBadge>
          </td>
          <td class="px-4 py-3 text-xs text-muted-foreground">{{ formatBytes(item.size_bytes) }}</td>
          <td class="px-4 py-3 font-mono text-xs text-muted-foreground">
            {{ item.digest ? item.digest.slice(0, 12) + '…' : '—' }}
          </td>
          <td class="px-4 py-3 text-xs text-muted-foreground">{{ formatRelative(item.updated_at) }}</td>
          <td class="px-4 py-3 text-right">
            <div class="inline-flex items-center gap-1">
              <NuxtLink
                :to="`/explore/${registrySlug}/packages/${encodeURIComponent(item.name)}`"
                class="inline-flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground"
                title="View in Explorer"
              >
                <ExternalLink class="h-3.5 w-3.5" />
              </NuxtLink>
              <button
                v-if="canDelete"
                type="button"
                class="inline-flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground hover:bg-red-50 hover:text-red-600"
                title="Delete artifact"
                @click="emit('delete', item)"
              >
                <Trash2 class="h-3.5 w-3.5" />
              </button>
            </div>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
