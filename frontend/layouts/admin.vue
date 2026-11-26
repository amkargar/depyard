<script setup lang="ts">
import {
  LayoutDashboard,
  Database,
  KeyRound,
  Users,
  ScrollText,
  Settings,
  Compass,
  LogOut,
  Menu,
  X,
  UserCircle,
} from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()
const { user, isAdmin, refresh, logout } = useAuth()
const mobileOpen = ref(false)
const authReady = ref(false)

const navItems = [
  { to: '/admin', label: 'Overview', icon: LayoutDashboard, exact: true },
  { to: '/admin/registries', label: 'Registries', icon: Database, exact: false },
  { to: '/admin/tokens', label: 'Access Tokens', icon: KeyRound, exact: false },
  { to: '/admin/users', label: 'Users', icon: Users, exact: false },
  { to: '/admin/audit', label: 'Audit Logs', icon: ScrollText, exact: false },
  { to: '/admin/settings', label: 'Settings', icon: Settings, exact: false },
]

// An administrator is also an ordinary account: they need the same personal
// pages every other user has (their own profile, password and personal
// tokens). These live under the user layout, so the admin console links out
// to them instead of duplicating them.
const accountItems = [
  { to: '/dashboard', label: 'My Dashboard', icon: LayoutDashboard, exact: true },
  { to: '/profile', label: 'My Profile', icon: UserCircle, exact: false },
  { to: '/tokens', label: 'My Tokens', icon: KeyRound, exact: false },
]

// Both lists share the same shape so the same isActive() helper can serve them.
type NavItem = (typeof navItems)[number]

function isActive(item: NavItem) {
  if (item.exact) return route.path === item.to
  return route.path === item.to || route.path.startsWith(item.to + '/')
}

onMounted(async () => {
  const authenticated = await refresh()
  if (!authenticated) {
    await router.replace(`/login?redirect=${encodeURIComponent(route.fullPath)}`)
    return
  }
  // A signed-in regular user is not banned from the console, they simply do
  // not belong in it: send them to their own dashboard instead of back to the
  // login page, which used to trap them in a redirect loop.
  if (!isAdmin.value) {
    await router.replace('/dashboard')
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
  <div v-if="authReady && isAdmin" class="min-h-screen flex bg-slate-50/70">
    <!-- Sidebar -->
    <aside
      :class="[
        'fixed inset-y-0 left-0 z-40 w-60 border-r bg-card flex flex-col transition-transform lg:static lg:translate-x-0',
        mobileOpen ? 'translate-x-0' : '-translate-x-full',
      ]"
    >
      <div class="h-14 border-b px-4 flex items-center justify-between">
        <NuxtLink to="/admin" class="flex items-center gap-2.5 font-semibold tracking-tight text-sm">
          <img src="/logo.png" alt="" width="36" height="36" class="h-9 w-9 shrink-0 object-contain" />
          <span>Depyard</span>
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

        <div class="px-3 pt-4">
          <div class="pb-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/70">
            My Account
          </div>
        </div>
        <NuxtLink
          v-for="item in accountItems"
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
            <div class="text-xs font-medium text-foreground truncate">{{ user?.username || 'admin' }}</div>
            <div class="text-[11px] text-muted-foreground truncate">{{ user?.email || 'Administrator' }}</div>
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
        <span class="text-sm font-semibold">Admin Console</span>
        <NuxtLink to="/" class="text-xs text-primary">Explorer</NuxtLink>
      </header>

      <main class="flex-1 p-4 sm:p-6 lg:p-8 max-w-7xl w-full mx-auto">
        <slot />
      </main>
    </div>
  </div>

  <div v-else class="min-h-screen flex items-center justify-center text-sm text-muted-foreground">
    Checking administrator session...
  </div>
</template>
