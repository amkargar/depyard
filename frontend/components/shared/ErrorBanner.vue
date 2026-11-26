<script setup lang="ts">
import { AlertCircle } from 'lucide-vue-next'

defineProps<{ message?: string | null }>()
</script>

<template>
  <UiAlert v-if="message" variant="destructive">
    <AlertCircle class="size-4" />
    <UiAlertDescription>{{ message }}</UiAlertDescription>
  </UiAlert>
</template>
