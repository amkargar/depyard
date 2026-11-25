<script setup lang="ts">
import { ChevronLeft, ChevronRight } from 'lucide-vue-next'

const props = defineProps<{
  page: number
  limit: number
  total: number
}>()

const emit = defineEmits<{
  (e: 'change', page: number): void
}>()

const totalPages = computed(() => Math.max(1, Math.ceil(props.total / props.limit)))
const startItem = computed(() => (props.total === 0 ? 0 : (props.page - 1) * props.limit + 1))
const endItem = computed(() => Math.min(props.total, props.page * props.limit))
</script>

<template>
  <div v-if="total > 0" class="flex items-center justify-between pt-3 text-xs text-muted-foreground">
    <span>Showing {{ startItem }}–{{ endItem }} of {{ total }}</span>
    <div class="flex items-center gap-1.5">
      <UiButton
        variant="outline"
        size="sm"
        :disabled="page <= 1"
        @click="emit('change', page - 1)"
      >
        <ChevronLeft class="h-3.5 w-3.5" />
        Prev
      </UiButton>
      <span class="px-2 font-medium text-foreground">Page {{ page }} / {{ totalPages }}</span>
      <UiButton
        variant="outline"
        size="sm"
        :disabled="page >= totalPages"
        @click="emit('change', page + 1)"
      >
        Next
        <ChevronRight class="h-3.5 w-3.5" />
      </UiButton>
    </div>
  </div>
</template>
