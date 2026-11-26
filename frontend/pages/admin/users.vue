<script setup lang="ts">
import { Plus, Trash2, Shield, User as UserIcon } from 'lucide-vue-next'
import type { User } from '~/types/registry'
import { formatDate } from '~/utils/format'

definePageMeta({ layout: 'admin' })

const { user: currentUser } = useAuth()
const loading = ref(true)
const errorMsg = ref('')
const users = ref<User[]>([])
const createOpen = ref(false)
const saving = ref(false)

const roleOptions = computed(() => [
  { value: 'user', label: 'User' },
  { value: 'admin', label: 'Administrator' },
])

const form = reactive({
  username: '',
  email: '',
  password: '',
  role: 'user' as 'admin' | 'user',
})

async function loadUsers() {
  loading.value = true
  errorMsg.value = ''
  try {
    const res = await apiFetch<{ users: User[] }>('/api/admin/users')
    users.value = res.users || []
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to load users'
  } finally {
    loading.value = false
  }
}

async function handleCreate() {
  saving.value = true
  errorMsg.value = ''
  try {
    await apiFetch('/api/admin/users', {
      method: 'POST',
      body: JSON.stringify(form),
    })
    createOpen.value = false
    form.username = ''
    form.email = ''
    form.password = ''
    form.role = 'user'
    await loadUsers()
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to create user'
  } finally {
    saving.value = false
  }
}

async function toggleDisabled(u: User) {
  await apiFetch(`/api/admin/users/${u.id}`, {
    method: 'PUT',
    body: JSON.stringify({ disabled: !u.disabled }),
  })
  await loadUsers()
}

async function handleDelete(u: User) {
  if (!confirm(`Delete user "${u.username}"?`)) return
  errorMsg.value = ''
  try {
    await apiFetch(`/api/admin/users/${u.id}`, { method: 'DELETE' })
    await loadUsers()
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to delete user'
  }
}

onMounted(loadUsers)
</script>

<template>
  <div class="space-y-6">
    <div class="flex flex-wrap items-center justify-between gap-4">
      <div>
        <h1 class="text-2xl font-bold tracking-tight">Users</h1>
        <p class="text-sm text-muted-foreground">Manage platform administrators and developer accounts.</p>
      </div>
      <UiButton size="sm" @click="createOpen = true">
        <Plus class="h-4 w-4" />
        Add User
      </UiButton>
    </div>

    <SharedErrorBanner :message="errorMsg" />

    <UiCard class="overflow-hidden">
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="border-b bg-muted/40 text-left text-xs font-medium text-muted-foreground">
              <th class="px-5 py-3">User</th>
              <th class="px-4 py-3">Role</th>
              <th class="px-4 py-3">Status</th>
              <th class="px-4 py-3">Created</th>
              <th class="px-4 py-3 text-right">Actions</th>
            </tr>
          </thead>
          <tbody class="divide-y">
            <tr v-if="loading">
              <td colspan="5" class="px-5 py-8 text-center text-muted-foreground">Loading users...</td>
            </tr>
            <tr v-for="u in users" :key="u.id" class="hover:bg-muted/20">
              <td class="px-5 py-3.5">
                <div class="font-medium text-foreground flex items-center gap-2">
                  <component
                    :is="u.role === 'admin' ? Shield : UserIcon"
                    class="h-3.5 w-3.5 text-muted-foreground"
                  />
                  {{ u.username }}
                </div>
                <div class="text-xs text-muted-foreground">{{ u.email || '—' }}</div>
              </td>
              <td class="px-4 py-3.5">
                <UiBadge :variant="u.role === 'admin' ? 'info' : 'secondary'" class="capitalize">
                  {{ u.role }}
                </UiBadge>
              </td>
              <td class="px-4 py-3.5">
                <UiBadge :variant="u.disabled ? 'destructive' : 'success'">
                  {{ u.disabled ? 'Disabled' : 'Active' }}
                </UiBadge>
              </td>
              <td class="px-4 py-3.5 text-xs text-muted-foreground">{{ formatDate(u.created_at) }}</td>
              <td class="px-4 py-3.5 text-right">
                <div class="inline-flex items-center gap-2">
                  <UiSwitch
                    v-if="u.id !== currentUser?.id"
                    :model-value="!u.disabled"
                    :aria-label="`Toggle ${u.username}`"
                    @update:model-value="toggleDisabled(u)"
                  />
                  <UiButton
                    v-if="u.id !== currentUser?.id"
                    variant="ghost"
                    size="sm"
                    @click="handleDelete(u)"
                  >
                    <Trash2 class="h-3.5 w-3.5 text-red-600" />
                  </UiButton>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </UiCard>

    <UiDialog :open="createOpen" title="Create User" @close="createOpen = false">
      <form id="user-form" class="space-y-4" @submit.prevent="handleCreate">
        <div class="space-y-1.5">
          <label class="text-xs font-medium">Username</label>
          <UiInput v-model="form.username" required />
        </div>
        <div class="space-y-1.5">
          <label class="text-xs font-medium">Email</label>
          <UiInput v-model="form.email" type="email" />
        </div>
        <div class="space-y-1.5">
          <label class="text-xs font-medium">Password (min 6 chars)</label>
          <UiInput v-model="form.password" type="password" required />
        </div>
        <div class="space-y-1.5">
          <label class="text-xs font-medium">Role</label>
          <UiSelect
            v-model="form.role"
            :options="roleOptions"
            aria-label="User role"
          />
        </div>
      </form>
      <template #footer>
        <UiButton variant="outline" @click="createOpen = false">Cancel</UiButton>
        <UiButton type="submit" form="user-form" :disabled="saving">
          {{ saving ? 'Creating...' : 'Create User' }}
        </UiButton>
      </template>
    </UiDialog>
  </div>
</template>
