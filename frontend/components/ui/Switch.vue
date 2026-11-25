<script setup lang="ts">
import { cn } from '~/utils/cn'

const props = withDefaults(
  defineProps<{
    modelValue: boolean
    disabled?: boolean
    ariaLabel?: string
  }>(),
  { disabled: false, ariaLabel: 'Toggle' },
)

const emit = defineEmits<{
  (e: 'update:modelValue', value: boolean): void
}>()
</script>

<template>
  <button
    type="button"
    role="switch"
    :aria-checked="props.modelValue"
    :aria-label="props.ariaLabel"
    :disabled="props.disabled"
    :class="
      cn(
        'peer inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border-2 border-transparent transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50',
        props.modelValue ? 'bg-primary' : 'bg-slate-200',
      )
    "
    @click="emit('update:modelValue', !props.modelValue)"
  >
    <span
      :class="
        cn(
          'pointer-events-none block h-4 w-4 rounded-full bg-white shadow-sm ring-0 transition-transform',
          props.modelValue ? 'translate-x-4' : 'translate-x-0',
        )
      "
    />
  </button>
</template>
