<script setup lang="ts">
import { Compass, KeyRound, UserCircle, Package, ArrowRight } from 'lucide-vue-next'
import type { PublicRegistryView } from '~/types/registry'

definePageMeta({ layout: 'user' })

const { user, isAdmin } = useAuth()
const loading = ref(true)
const errorMsg = ref('')
const registries = ref<PublicRegistryView[]>([])
const tokenCount = ref(0)

async function load() {
  loading.value = true
  errorMsg.value = ''
  try {
    const [dir, tokens] = await Promise.all([
      apiFetch<{ registries: PublicRegistryView[] }>('/api/public/registries'),
      apiFetch<{ tokens: unknown[] }>('/api/admin/my/tokens').catch(() => ({ tokens: [] })),
    ])
    registries.value = dir.registries || []
    tokenCount.value = (tokens.tokens || []).length
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to load dashboard data'
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="space-y-6">
    <div>
      <h1 class="text-2xl font-bold tracking-tight">
        Welcome back, {{ user?.username }}
      </h1>
      <p class="text-sm text-muted-foreground">
        Manage your account, personal access tokens, and the registries available on this instance.
      </p>
    </div>

    <SharedErrorBanner :message="errorMsg" />

    <div class="grid gap-4 sm:grid-cols-3">
      <NuxtLink to="/profile" class="group">
        <UiCard class="h-full p-5 transition-colors hover:border-primary/40">
          <div class="flex items-center gap-2.5 text-xs font-medium text-muted-foreground">
            <UserCircle class="h-4 w-4" />
            Profile
          </div>
          <p class="mt-2 text-sm text-foreground">Edit your username and email address.</p>
        </UiCard>
      </NuxtLink>

      <NuxtLink to="/tokens" class="group">
        <UiCard class="h-full p-5 transition-colors hover:border-primary/40">
          <div class="flex items-center gap-2.5 text-xs font-medium text-muted-foreground">
            <KeyRound class="h-4 w-4" />
            Personal Tokens
          </div>
          <p class="mt-2 text-sm text-foreground">
            {{ tokenCount }} active token{{ tokenCount === 1 ? '' : 's' }} — create scoped API credentials.
          </p>
        </UiCard>
      </NuxtLink>

      <NuxtLink to="/" class="group">
        <UiCard class="h-full p-5 transition-colors hover:border-primary/40">
          <div class="flex items-center gap-2.5 text-xs font-medium text-muted-foreground">
            <Compass class="h-4 w-4" />
            Package Explorer
          </div>
          <p class="mt-2 text-sm text-foreground">Browse the public registries hosted here.</p>
        </UiCard>
      </NuxtLink>
    </div>

    <UiCard class="overflow-hidden">
      <div class="border-b px-5 py-4 flex items-center gap-2">
        <Package class="h-4 w-4 text-muted-foreground" />
        <h2 class="text-sm font-semibold">Available registries</h2>
      </div>

      <div v-if="loading" class="px-5 py-8 text-center text-sm text-muted-foreground">
        Loading registries...
      </div>

      <div v-else-if="!registries.length" class="px-5 py-8 text-center text-sm text-muted-foreground">
        No registries are published on this instance yet.
      </div>

      <ul v-else class="divide-y">
        <li v-for="reg in registries" :key="reg.id" class="flex items-center justify-between gap-4 px-5 py-3.5">
          <div class="min-w-0">
            <div class="flex items-center gap-2">
              <span class="font-medium text-sm text-foreground truncate">{{ reg.name }}</span>
              <UiBadge variant="secondary" class="font-mono text-[11px]">{{ reg.type }}</UiBadge>
              <UiBadge v-if="reg.visibility === 'public'" variant="info">Public</UiBadge>
              <UiBadge v-else variant="warning">Private</UiBadge>
            </div>
            <div class="mt-0.5 text-xs text-muted-foreground font-mono">{{ reg.base_path }}</div>
          </div>
          <NuxtLink
            :to="`/explore/${reg.slug}`"
            class="inline-flex items-center gap-1 text-xs font-medium text-primary hover:underline shrink-0"
          >
            Explore
            <ArrowRight class="h-3 w-3" />
          </NuxtLink>
        </li>
      </ul>
    </UiCard>

    <UiCard v-if="isAdmin" class="p-5">
      <h2 class="text-sm font-semibold">Administrator</h2>
      <p class="mt-1 text-xs text-muted-foreground">
        Your account has administrator privileges on this instance.
      </p>
      <NuxtLink
        to="/admin"
        class="mt-3 inline-flex items-center gap-1.5 text-xs font-medium text-primary hover:underline"
      >
        Open the Admin Console
        <ArrowRight class="h-3 w-3" />
      </NuxtLink>
    </UiCard>
  </div>
</template>
