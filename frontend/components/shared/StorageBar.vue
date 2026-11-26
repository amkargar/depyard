<script setup lang="ts">
import { formatBytes } from '~/utils/format'

const props = defineProps<{
  usedBytes: number
  quotaBytes: number
  hostedBytes?: number
  cachedBytes?: number
  compact?: boolean
}>()

const pct = computed(() => {
  if (!props.quotaBytes || props.quotaBytes <= 0) return 0
  return Math.min(100, (props.usedBytes / props.quotaBytes) * 100)
})

const barColor = computed(() => {
  if (pct.value >= 90) return 'bg-red-500'
  if (pct.value >= 75) return 'bg-amber-500'
  return 'bg-primary'
})
</script>

<template>
  <div class="space-y-1.5">
    <div class="flex items-center justify-between text-xs">
      <span class="font-medium text-foreground">{{ formatBytes(usedBytes) }}</span>
      <span class="text-muted-foreground">
        {{ quotaBytes > 0 ? `of ${formatBytes(quotaBytes)} (${pct.toFixed(0)}%)` : 'No quota limit' }}
      </span>
    </div>
    <UiProgress v-if="quotaBytes > 0" :model-value="pct" :bar-class="barColor" />
    <div v-if="!compact && (hostedBytes || cachedBytes)" class="flex items-center gap-3 text-[11px] text-muted-foreground">
      <span>Hosted: <strong class="text-foreground">{{ formatBytes(hostedBytes) }}</strong></span>
      <span>Cached: <strong class="text-foreground">{{ formatBytes(cachedBytes) }}</strong></span>
    </div>
  </div>
</template>
