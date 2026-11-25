<script setup lang="ts">
import { Boxes, Lock } from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()
const { login, refresh } = useAuth()

const username = ref('admin')
const password = ref('')
const loading = ref(false)
const errorMsg = ref('')

onMounted(async () => {
  if (await refresh()) {
    await router.replace('/admin')
  }
})

async function handleSubmit() {
  loading.value = true
  errorMsg.value = ''
  try {
    await login(username.value, password.value)
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/admin'
    await router.push(redirect.startsWith('/') ? redirect : '/admin')
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
        <h1 class="text-xl font-bold tracking-tight">Admin Console Sign In</h1>
        <p class="text-xs text-muted-foreground">
          Manage registries, scoped access tokens, cache policies, and platform monitoring.
        </p>
      </div>

      <form class="space-y-4" @submit.prevent="handleSubmit">
        <SharedErrorBanner :message="errorMsg" />

        <div class="space-y-1.5">
          <label class="text-xs font-medium text-foreground">Username</label>
          <UiInput v-model="username" placeholder="admin" required />
        </div>

        <div class="space-y-1.5">
          <label class="text-xs font-medium text-foreground">Password</label>
          <UiInput v-model="password" type="password" placeholder="••••••••" required />
        </div>

        <UiButton type="submit" class="w-full" :disabled="loading">
          <Lock class="h-3.5 w-3.5" />
          {{ loading ? 'Signing in...' : 'Sign In' }}
        </UiButton>
      </form>
    </UiCard>
  </div>
</template>
