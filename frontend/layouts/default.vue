<script setup lang="ts">
import { Boxes, Shield, Compass, UserCircle } from 'lucide-vue-next'

const { user, isAdmin, refresh } = useAuth()
onMounted(() => refresh())
</script>

<template>
  <div class="min-h-screen flex flex-col bg-background">
    <header class="sticky top-0 z-30 border-b bg-background/95 backdrop-blur supports-[backdrop-filter]:bg-background/80">
      <div class="mx-auto max-w-6xl px-4 sm:px-6 h-14 flex items-center justify-between gap-4">
        <div class="flex items-center gap-6">
          <NuxtLink to="/" class="flex items-center gap-2.5 font-semibold tracking-tight text-foreground">
            <div class="flex h-8 w-8 items-center justify-center rounded-lg bg-[#122030] text-sky-400">
              <Boxes class="h-4 w-4" />
            </div>
            <span>Depyard</span>
          </NuxtLink>

          <nav class="hidden sm:flex items-center gap-1">
            <NuxtLink
              to="/"
              class="inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm text-muted-foreground hover:bg-muted hover:text-foreground transition-colors"
              active-class="bg-muted text-foreground font-medium"
            >
              <Compass class="h-3.5 w-3.5" />
              Registries
            </NuxtLink>
          </nav>
        </div>

        <div class="flex items-center gap-2">
          <template v-if="user">
            <NuxtLink
              to="/dashboard"
              class="inline-flex items-center gap-1.5 rounded-md border bg-card px-3 py-1.5 text-xs font-medium text-foreground shadow-sm hover:bg-muted transition-colors"
            >
              <UserCircle class="h-3.5 w-3.5 text-primary" />
              My Account
            </NuxtLink>
            <NuxtLink
              v-if="isAdmin"
              to="/admin"
              class="inline-flex items-center gap-1.5 rounded-md border bg-card px-3 py-1.5 text-xs font-medium text-foreground shadow-sm hover:bg-muted transition-colors"
            >
              <Shield class="h-3.5 w-3.5 text-primary" />
              Admin Console
            </NuxtLink>
          </template>
          <NuxtLink
            v-else
            to="/login"
            class="inline-flex items-center gap-1.5 rounded-md border bg-card px-3 py-1.5 text-xs font-medium text-foreground shadow-sm hover:bg-muted transition-colors"
          >
            <Shield class="h-3.5 w-3.5 text-primary" />
            Sign In
          </NuxtLink>
        </div>
      </div>
    </header>

    <main class="flex-1">
      <slot />
    </main>

    <footer class="border-t bg-card/50 py-6 text-xs text-muted-foreground">
      <div class="mx-auto max-w-6xl px-4 sm:px-6 flex flex-col sm:flex-row items-center justify-between gap-2">
        <span>Depyard — Self-hosted Multi-Format Package &amp; Artifact Registry Platform</span>
        <span class="font-mono text-[11px]">Multi-registry routing: /r/&lt;slug&gt;</span>
      </div>
    </footer>
  </div>
</template>
