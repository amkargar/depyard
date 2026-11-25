<script setup lang="ts">
import { cn } from '~/utils/cn'

const props = withDefaults(
  defineProps<{
    value: number // 0 to 100
    barClass?: string
    class?: string
  }>(),
  { barClass: 'bg-primary', class: '' },
)

const clamped = computed(() => Math.min(100, Math.max(0, props.value)))
</script>

<template>
  <div :class="cn('relative h-2 w-full overflow-hidden rounded-full bg-slate-100', props.class)">
    <div
      :class="cn('h-full transition-all duration-300 rounded-full', props.barClass)"
      :style="{ width: `${clamped}%` }"
    />
  </div>
</template>
