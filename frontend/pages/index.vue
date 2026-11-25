<script setup lang="ts">
import { ArrowRight, Package, Download, Globe, Lock, Boxes, Shield } from 'lucide-vue-next'
import type { PublicRegistryView } from '~/types/registry'

interface DirectoryResponse {
  registries: PublicRegistryView[]
  site_title: string
  enable_root_compat_route: boolean
}

const loading = ref(true)
const errorMsg = ref('')
const registries = ref<PublicRegistryView[]>([])
const rootCompat = ref(true)

const defaultRegistry = computed(
  () => registries.value.find((r) => r.is_default) ?? registries.value[0] ?? null,
)

onMounted(async () => {
  try {
    const res = await apiFetch<DirectoryResponse>('/api/public/registries')
    registries.value = res.registries || []
    rootCompat.value = res.enable_root_compat_route
  } catch (err: any) {
    errorMsg.value = err?.message || 'Unable to load registry directory'
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div>
    <!-- Hero header -->
    <section class="bg-[#122030] text-white border-b border-slate-800">
      <div class="mx-auto max-w-6xl px-4 sm:px-6 py-12 sm:py-16">
        <div class="max-w-3xl">
          <div class="inline-flex items-center gap-2 rounded-full bg-sky-500/15 border border-sky-400/25 px-3 py-1 text-xs font-medium text-sky-300 mb-4">
            <Boxes class="h-3.5 w-3.5" />
            Self-Hosted Multi-Registry Platform
          </div>
          <h1 class="text-3xl sm:text-4xl font-bold tracking-tight">
            Package &amp; Artifact Registries
          </h1>
          <p class="mt-3 text-base text-slate-300 leading-relaxed">
            Lightweight, isolated multi-format package hosting and caching mirror. Browse available repositories, inspect package versions and checksums, or manage instances from the Admin Console.
          </p>

          <div class="mt-6 flex flex-wrap items-center gap-3">
            <NuxtLink
              v-if="defaultRegistry"
              :to="`/explore/${defaultRegistry.slug}`"
              class="inline-flex items-center gap-2 rounded-lg bg-[#0175c2] hover:bg-[#0284d9] px-5 py-2.5 text-sm font-medium text-white shadow-sm transition-colors"
            >
              Explore {{ defaultRegistry.name }}
              <ArrowRight class="h-4 w-4" />
            </NuxtLink>
            <NuxtLink
              to="/admin"
              class="inline-flex items-center gap-2 rounded-lg bg-white/10 hover:bg-white/15 border border-white/15 px-5 py-2.5 text-sm font-medium text-white transition-colors"
            >
              <Shield class="h-4 w-4" />
              Open Admin Console
            </NuxtLink>
          </div>
        </div>
      </div>
    </section>

    <!-- Configured Registries Directory -->
    <section class="mx-auto max-w-6xl px-4 sm:px-6 py-10">
      <div class="flex flex-wrap items-end justify-between gap-4 mb-6">
        <div>
          <h2 class="text-xl font-semibold tracking-tight text-foreground">Available Registries</h2>
          <p class="text-sm text-muted-foreground mt-0.5">
            Each registry instance operates under its own isolated base URL <code class="font-mono text-xs">/r/&lt;slug&gt;</code>.
          </p>
        </div>
        <UiBadge v-if="rootCompat && defaultRegistry" variant="success">
          Root compatibility route enabled → {{ defaultRegistry.slug }}
        </UiBadge>
      </div>

      <SharedErrorBanner :message="errorMsg" class="mb-6" />

      <div v-if="loading" class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
        <div v-for="n in 3" :key="n" class="h-44 rounded-xl border bg-card animate-pulse" />
      </div>

      <SharedEmptyState
        v-else-if="registries.length === 0"
        title="No public registries available"
        description="Sign in to the Admin Console to create or enable a registry instance."
      >
        <template #action>
          <NuxtLink to="/admin/registries">
            <UiButton>Configure Registries</UiButton>
          </NuxtLink>
        </template>
      </SharedEmptyState>

      <div v-else class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
        <NuxtLink
          v-for="reg in registries"
          :key="reg.id"
          :to="`/explore/${reg.slug}`"
          class="group flex flex-col justify-between rounded-xl border bg-card p-5 hover:border-primary/50 hover:shadow-md transition-all"
        >
          <div>
            <div class="flex items-center justify-between gap-2 mb-3">
              <div class="flex items-center gap-1.5">
                <UiBadge variant="info" class="uppercase font-mono text-[10px]">{{ reg.type }}</UiBadge>
                <UiBadge variant="secondary" class="capitalize">{{ reg.mode }}</UiBadge>
                <UiBadge v-if="reg.is_default" variant="success">Default</UiBadge>
              </div>
              <component
                :is="reg.visibility === 'public' ? Globe : Lock"
                class="h-4 w-4 text-muted-foreground"
                :title="reg.visibility"
              />
            </div>

            <h3 class="text-base font-semibold text-foreground group-hover:text-primary transition-colors">
              {{ reg.name }}
            </h3>
            <p class="mt-1 text-xs text-muted-foreground line-clamp-2 min-h-[2rem]">
              {{ reg.description || 'Self-hosted package repository instance.' }}
            </p>
            <div class="mt-3 rounded bg-muted/60 px-2.5 py-1.5 font-mono text-[11px] text-slate-700 truncate">
              {{ reg.base_path }}
            </div>
          </div>

          <div class="mt-4 pt-3 border-t flex items-center justify-between text-xs text-muted-foreground">
            <div class="flex items-center gap-3">
              <span class="inline-flex items-center gap-1">
                <Package class="h-3.5 w-3.5" />
                {{ reg.artifact_count }}
              </span>
              <span class="inline-flex items-center gap-1">
                <Download class="h-3.5 w-3.5" />
                {{ reg.downloads }}
              </span>
            </div>
            <span class="inline-flex items-center gap-1 font-medium text-primary">
              Browse
              <ArrowRight class="h-3.5 w-3.5 transition-transform group-hover:translate-x-0.5" />
            </span>
          </div>
        </NuxtLink>
      </div>
    </section>
  </div>
</template>
