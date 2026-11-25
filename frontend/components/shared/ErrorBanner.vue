<script setup lang="ts">
import { AlertCircle } from 'lucide-vue-next'

defineProps<{ message?: string | null }>()
</script>

<template>
  <div
    v-if="message"
    class="flex items-start gap-2.5 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700"
  >
    <AlertCircle class="h-4 w-4 shrink-0 mt-0.5" />
    <span>{{ message }}</span>
  </div>
</template>
