<script setup lang="ts">
import {
  Boxes,
  LayoutDashboard,
  UserCircle,
  KeyRound,
  Compass,
  LogOut,
  Menu,
  X,
  Shield,
} from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()
const { user, isAdmin, refresh, logout } = useAuth()
const mobileOpen = ref(false)
const authReady = ref(false)

const navItems = [
  { to: '/dashboard', label: 'Dashboard', icon: LayoutDashboard, exact: true },
  { to: '/profile', label: 'Profile', icon: UserCircle, exact: false },
  { to: '/tokens', label: 'My Tokens', icon: KeyRound, exact: false },
]

function isActive(item: (typeof navItems)[number]) {
  if (item.exact) return route.path === item.to
  return route.path === item.to || route.path.startsWith(item.to + '/')
}

onMounted(async () => {
  const authenticated = await refresh()
  if (!authenticated) {
    await router.replace(`/login?redirect=${encodeURIComponent(route.fullPath)}`)
    return
  }
  authReady.value = true
})

async function handleLogout() {
  await logout()
  await router.push('/login')
}
</script>

<template>
  <div v-if="authReady" class="min-h-screen flex bg-slate-50/70">
    <!-- Sidebar -->
    <aside
      :class="[
        'fixed inset-y-0 left-0 z-40 w-60 border-r bg-card flex flex-col transition-transform lg:static lg:translate-x-0',
        mobileOpen ? 'translate-x-0' : '-translate-x-full',
      ]"
    >
      <div class="h-14 border-b px-4 flex items-center justify-between">
        <NuxtLink to="/dashboard" class="flex items-center gap-2.5 font-semibold tracking-tight text-sm">
          <div class="flex h-7 w-7 items-center justify-center rounded-lg bg-[#122030] text-sky-400">
            <Boxes class="h-4 w-4" />
          </div>
          <span>Mirror Registry</span>
        </NuxtLink>
        <button type="button" class="lg:hidden p-1 text-muted-foreground" @click="mobileOpen = false">
          <X class="h-4 w-4" />
        </button>
      </div>

      <nav class="flex-1 px-3 py-4 space-y-1 overflow-y-auto">
        <NuxtLink
          v-for="item in navItems"
          :key="item.to"
          :to="item.to"
          :class="[
            'flex items-center gap-2.5 rounded-md px-3 py-2 text-sm font-medium transition-colors',
            isActive(item)
              ? 'bg-primary/10 text-primary'
              : 'text-slate-600 hover:bg-muted hover:text-foreground',
          ]"
          @click="mobileOpen = false"
        >
          <component :is="item.icon" class="h-4 w-4 shrink-0" />
          <span>{{ item.label }}</span>
        </NuxtLink>

        <NuxtLink
          v-if="isAdmin"
          to="/admin"
          class="flex items-center gap-2.5 rounded-md px-3 py-2 text-sm font-medium text-slate-600 hover:bg-muted hover:text-foreground transition-colors"
          @click="mobileOpen = false"
        >
          <Shield class="h-4 w-4 shrink-0" />
          <span>Admin Console</span>
        </NuxtLink>
      </nav>

      <div class="border-t p-3 space-y-2">
        <NuxtLink
          to="/"
          class="flex items-center gap-2 rounded-md px-3 py-2 text-xs font-medium text-muted-foreground hover:bg-muted hover:text-foreground transition-colors"
        >
          <Compass class="h-3.5 w-3.5" />
          Open Package Explorer
        </NuxtLink>
        <div class="flex items-center justify-between rounded-md bg-muted/50 px-3 py-2">
          <div class="min-w-0">
            <div class="text-xs font-medium text-foreground truncate">{{ user?.username }}</div>
            <div class="text-[11px] text-muted-foreground truncate">{{ user?.email || 'Account' }}</div>
          </div>
          <button
            type="button"
            class="rounded p-1 text-muted-foreground hover:bg-background hover:text-foreground"
            title="Sign out"
            @click="handleLogout"
          >
            <LogOut class="h-3.5 w-3.5" />
          </button>
        </div>
      </div>
    </aside>

    <!-- Main area -->
    <div class="flex-1 flex flex-col min-w-0">
      <header class="h-14 border-b bg-card px-4 sm:px-6 flex items-center justify-between lg:hidden">
        <button type="button" class="p-1.5 rounded-md border" @click="mobileOpen = true">
          <Menu class="h-4 w-4" />
        </button>
        <span class="text-sm font-semibold">My Account</span>
        <NuxtLink to="/" class="text-xs text-primary">Explorer</NuxtLink>
      </header>

      <main class="flex-1 p-4 sm:p-6 lg:p-8 max-w-5xl w-full mx-auto">
        <slot />
      </main>
    </div>
  </div>

  <div v-else class="min-h-screen flex items-center justify-center text-sm text-muted-foreground">
    Checking your session...
  </div>
</template>
