<script setup lang="ts">
import { Boxes, Lock } from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()
const { login, refresh, isAdmin, user } = useAuth()

const username = ref('')
const password = ref('')
const loading = ref(false)
const errorMsg = ref('')

onMounted(async () => {
  const authenticated = await refresh()
  if (authenticated) {
    await router.replace(destinationFor())
  }
})

// destinationFor picks the landing page for the signed-in account.
//
// Administrators continue into the admin console while regular users are sent
// to their own dashboard; previously everyone was pushed to /admin, which
// bounced non-admins straight back to this page.
function destinationFor(): string {
  const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : ''
  if (redirect.startsWith('/') && !redirect.startsWith('//')) {
    return redirect
  }
  return isAdmin.value ? '/admin' : '/dashboard'
}

async function handleSubmit() {
  loading.value = true
  errorMsg.value = ''
  try {
    await login(username.value, password.value)
    await router.push(destinationFor())
  } catch (err: any) {
    errorMsg.value = err?.message || 'Sign in failed'
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="mx-auto max-w-md px-4 py-16">
    <UiCard class="p-7 space-y-6">
      <div class="text-center space-y-2">
        <div class="mx-auto flex h-11 w-11 items-center justify-center rounded-xl bg-[#122030] text-sky-400">
          <Boxes class="h-5 w-5" />
        </div>
        <h1 class="text-xl font-bold tracking-tight">Sign In</h1>
        <p class="text-xs text-muted-foreground">
          Access your personal dashboard, personal access tokens, and — for administrators — the
          registry console.
        </p>
      </div>

      <form class="space-y-4" @submit.prevent="handleSubmit">
        <SharedErrorBanner :message="errorMsg" />

        <div class="space-y-1.5">
          <label class="text-xs font-medium text-foreground">Username</label>
          <UiInput v-model="username" autocomplete="username" placeholder="your username" required />
        </div>

        <div class="space-y-1.5">
          <label class="text-xs font-medium text-foreground">Password</label>
          <UiInput
            v-model="password"
            type="password"
            autocomplete="current-password"
            placeholder="••••••••"
            required
          />
        </div>

        <UiButton type="submit" class="w-full" :disabled="loading">
          <Lock class="h-3.5 w-3.5" />
          {{ loading ? 'Signing in...' : 'Sign In' }}
        </UiButton>
      </form>

      <p v-if="user" class="text-center text-[11px] text-muted-foreground">
        Already signed in as {{ user.username }}.
      </p>
    </UiCard>
  </div>
</template>
