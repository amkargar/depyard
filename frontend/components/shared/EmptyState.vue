<script setup lang="ts">
import { PackageOpen } from 'lucide-vue-next'

withDefaults(
  defineProps<{
    title: string
    description?: string
  }>(),
  { description: '' },
)
</script>

<template>
  <div class="flex flex-col items-center justify-center rounded-lg border border-dashed py-12 px-6 text-center">
    <div class="rounded-full bg-muted p-3 mb-3 text-muted-foreground">
      <slot name="icon">
        <PackageOpen class="h-6 w-6" />
      </slot>
    </div>
    <h3 class="text-sm font-semibold text-foreground">{{ title }}</h3>
    <p v-if="description" class="mt-1 max-w-md text-xs text-muted-foreground">{{ description }}</p>
    <div v-if="$slots.action" class="mt-4">
      <slot name="action" />
    </div>
  </div>
</template>
