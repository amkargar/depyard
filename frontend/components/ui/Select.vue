<script setup lang="ts">
import { cn } from '~/utils/cn'

const props = withDefaults(
  defineProps<{
    modelValue?: string
    disabled?: boolean
    class?: string
  }>(),
  { modelValue: '', disabled: false, class: '' },
)

const emit = defineEmits<{
  (e: 'update:modelValue', value: string): void
}>()
</script>

<template>
  <select
    :value="props.modelValue"
    :disabled="props.disabled"
    :class="
      cn(
        'flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50',
        props.class,
      )
    "
    @change="emit('update:modelValue', ($event.target as HTMLSelectElement).value)"
  >
    <slot />
  </select>
</template>
