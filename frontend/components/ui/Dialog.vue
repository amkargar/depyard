<script setup lang="ts">
import { X } from 'lucide-vue-next'

const props = withDefaults(
  defineProps<{
    open: boolean
    title: string
    description?: string
    maxWidthClass?: string
  }>(),
  { description: '', maxWidthClass: 'max-w-xl' },
)

const emit = defineEmits<{
  (e: 'close'): void
}>()
</script>

<template>
  <Teleport to="body">
    <div
      v-if="props.open"
      class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 backdrop-blur-[1px] p-4"
      @click.self="emit('close')"
    >
      <div
        :class="[
          'w-full rounded-xl border bg-background shadow-2xl max-h-[90vh] flex flex-col overflow-hidden',
          props.maxWidthClass,
        ]"
      >
        <div class="flex items-start justify-between gap-4 border-b px-6 py-4">
          <div>
            <h2 class="text-lg font-semibold tracking-tight text-foreground">{{ props.title }}</h2>
            <p v-if="props.description" class="text-sm text-muted-foreground mt-0.5">{{ props.description }}</p>
          </div>
          <button
            type="button"
            class="rounded-md p-1 text-muted-foreground hover:bg-muted hover:text-foreground transition-colors"
            @click="emit('close')"
          >
            <X class="h-4 w-4" />
          </button>
        </div>
        <div class="overflow-y-auto px-6 py-5 flex-1">
          <slot />
        </div>
        <div v-if="$slots.footer" class="border-t bg-muted/30 px-6 py-3.5 flex items-center justify-end gap-2">
          <slot name="footer" />
        </div>
      </div>
    </div>
  </Teleport>
</template>
