<script setup lang="ts">
import { UserCircle, KeyRound, Save } from 'lucide-vue-next'
import type { User } from '~/types/registry'

definePageMeta({ layout: 'user' })

const { user, isAdmin, refresh } = useAuth()

const profileForm = reactive({ username: '', email: '' })
const passwordForm = reactive({ current_password: '', new_password: '', confirm_password: '' })

const loadingProfile = ref(true)
const savingProfile = ref(false)
const savingPassword = ref(false)
const profileError = ref('')
const profileSuccess = ref('')
const passwordError = ref('')
const passwordSuccess = ref('')
const minPasswordLength = ref(12)

async function loadProfile() {
  loadingProfile.value = true
  profileError.value = ''
  try {
    const res = await apiFetch<{ user: User; min_password_length: number }>('/api/admin/profile')
    profileForm.username = res.user?.username ?? ''
    profileForm.email = res.user?.email ?? ''
    minPasswordLength.value = res.min_password_length || 12
  } catch (err: any) {
    profileError.value = err?.message || 'Failed to load your profile'
  } finally {
    loadingProfile.value = false
  }
}

async function saveProfile() {
  savingProfile.value = true
  profileError.value = ''
  profileSuccess.value = ''
  try {
    await apiFetch('/api/admin/profile', {
      method: 'PUT',
      body: JSON.stringify({ username: profileForm.username, email: profileForm.email }),
    })
    profileSuccess.value = 'Profile updated.'
    await refresh(true)
  } catch (err: any) {
    profileError.value = err?.message || 'Failed to update your profile'
  } finally {
    savingProfile.value = false
  }
}

async function changePassword() {
  passwordError.value = ''
  passwordSuccess.value = ''

  if (passwordForm.new_password !== passwordForm.confirm_password) {
    passwordError.value = 'The new passwords do not match.'
    return
  }
  if (passwordForm.new_password.length < minPasswordLength.value) {
    passwordError.value = `The new password must be at least ${minPasswordLength.value} characters.`
    return
  }

  savingPassword.value = true
  try {
    await apiFetch('/api/admin/profile/password', {
      method: 'POST',
      body: JSON.stringify({
        current_password: passwordForm.current_password,
        new_password: passwordForm.new_password,
      }),
    })
    passwordSuccess.value =
      'Password changed. Every other session and personal token has been revoked — sign in again.'
    passwordForm.current_password = ''
    passwordForm.new_password = ''
    passwordForm.confirm_password = ''
  } catch (err: any) {
    passwordError.value = err?.message || 'Failed to change your password'
  } finally {
    savingPassword.value = false
  }
}

onMounted(loadProfile)
</script>

<template>
  <div class="space-y-6">
    <div>
      <h1 class="text-2xl font-bold tracking-tight">Profile</h1>
      <p class="text-sm text-muted-foreground">
        Update your account details and change your password.
      </p>
    </div>

    <div class="grid gap-6 lg:grid-cols-2">
      <!-- Account details -->
      <UiCard class="p-5 space-y-4">
        <div class="flex items-center gap-2">
          <UserCircle class="h-4 w-4 text-muted-foreground" />
          <h2 class="text-sm font-semibold">Account details</h2>
        </div>

        <div v-if="loadingProfile" class="text-sm text-muted-foreground">Loading your profile...</div>

        <template v-else>
          <SharedErrorBanner :message="profileError" />

          <div class="space-y-1.5">
            <label class="text-xs font-medium">Username</label>
            <UiInput v-model="profileForm.username" autocomplete="username" />
          </div>

          <div class="space-y-1.5">
            <label class="text-xs font-medium">Email</label>
            <UiInput v-model="profileForm.email" type="email" autocomplete="email" />
          </div>

          <div class="space-y-1.5">
            <label class="text-xs font-medium">Role</label>
            <UiInput :model-value="isAdmin ? 'Administrator' : 'User'" disabled />
            <p class="text-[11px] text-muted-foreground">
              Only an administrator can change your role or account status.
            </p>
          </div>

          <UiButton :disabled="savingProfile" @click="saveProfile">
            <Save class="h-3.5 w-3.5" />
            {{ savingProfile ? 'Saving...' : 'Save changes' }}
          </UiButton>

          <p v-if="profileSuccess" class="text-xs text-emerald-600">{{ profileSuccess }}</p>
        </template>
      </UiCard>

      <!-- Password -->
      <UiCard class="p-5 space-y-4">
        <div class="flex items-center gap-2">
          <KeyRound class="h-4 w-4 text-muted-foreground" />
          <h2 class="text-sm font-semibold">Change password</h2>
        </div>

        <SharedErrorBanner :message="passwordError" />

        <div class="space-y-1.5">
          <label class="text-xs font-medium">Current password</label>
          <UiInput
            v-model="passwordForm.current_password"
            type="password"
            autocomplete="current-password"
          />
        </div>

        <div class="space-y-1.5">
          <label class="text-xs font-medium">New password</label>
          <UiInput v-model="passwordForm.new_password" type="password" autocomplete="new-password" />
          <p class="text-[11px] text-muted-foreground">
            Minimum {{ minPasswordLength }} characters.
          </p>
        </div>

        <div class="space-y-1.5">
          <label class="text-xs font-medium">Confirm new password</label>
          <UiInput v-model="passwordForm.confirm_password" type="password" autocomplete="new-password" />
        </div>

        <UiButton :disabled="savingPassword" @click="changePassword">
          {{ savingPassword ? 'Updating...' : 'Change password' }}
        </UiButton>

        <p v-if="passwordSuccess" class="text-xs text-emerald-600">{{ passwordSuccess }}</p>

        <p class="text-[11px] text-muted-foreground">
          Changing your password signs out every other session and revokes all personal tokens.
        </p>
      </UiCard>
    </div>
  </div>
</template>
