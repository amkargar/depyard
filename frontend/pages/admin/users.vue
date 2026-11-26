<script setup lang="ts">
import { Plus, Trash2, Shield, User as UserIcon, Pencil } from 'lucide-vue-next'
import type { User } from '~/types/registry'
import { formatDate } from '~/utils/format'

definePageMeta({ layout: 'admin' })

const { user: currentUser } = useAuth()
const loading = ref(true)
const errorMsg = ref('')
const users = ref<User[]>([])
const createOpen = ref(false)
const saving = ref(false)
const editOpen = ref(false)
const editing = ref<User | null>(null)
const minPasswordLength = ref(12)

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

const editForm = reactive({
  username: '',
  email: '',
  password: '',
  role: 'user' as 'admin' | 'user',
  disabled: false,
})

async function loadUsers() {
  loading.value = true
  errorMsg.value = ''
  try {
    const res = await apiFetch<{ users: User[] }>('/api/admin/users')
    users.value = res.users || []
    const profile = await apiFetch<{ min_password_length: number }>('/api/admin/profile').catch(() => null)
    if (profile?.min_password_length) minPasswordLength.value = profile.min_password_length
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
  errorMsg.value = ''
  try {
    await apiFetch(`/api/admin/users/${u.id}`, {
      method: 'PUT',
      body: JSON.stringify({ disabled: !u.disabled }),
    })
    await loadUsers()
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to update the account'
  }
}

function openEdit(u: User) {
  editing.value = u
  editForm.username = u.username
  editForm.email = u.email
  editForm.password = ''
  editForm.role = u.role
  editForm.disabled = u.disabled
  errorMsg.value = ''
  editOpen.value = true
}

async function handleEdit() {
  if (!editing.value) return
  saving.value = true
  errorMsg.value = ''
  try {
    const payload: Record<string, unknown> = {
      username: editForm.username.trim(),
      email: editForm.email.trim(),
      role: editForm.role,
      disabled: editForm.disabled,
    }
    // Only send a password when the administrator actually typed one, so that
    // editing a user does not silently reset their password.
    if (editForm.password) payload.password = editForm.password
    await apiFetch(`/api/admin/users/${editing.value.id}`, {
      method: 'PUT',
      body: JSON.stringify(payload),
    })
    editOpen.value = false
    editing.value = null
    await loadUsers()
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to update the user'
  } finally {
    saving.value = false
  }
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
                  <UiButton
                    variant="ghost"
                    size="sm"
                    title="Edit user"
                    @click="openEdit(u)"
                  >
                    <Pencil class="h-3.5 w-3.5" />
                  </UiButton>
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

    <UiDialog
      :open="editOpen"
      :title="`Edit ${editing?.username || 'user'}`"
      description="Changing a password, role, or status invalidates that user's sessions and personal tokens."
      @close="editOpen = false"
    >
      <form id="user-edit-form" class="space-y-4" @submit.prevent="handleEdit">
        <div class="space-y-1.5">
          <label class="text-xs font-medium">Username</label>
          <UiInput v-model="editForm.username" required />
        </div>
        <div class="space-y-1.5">
          <label class="text-xs font-medium">Email</label>
          <UiInput v-model="editForm.email" type="email" />
        </div>
        <div class="space-y-1.5">
          <label class="text-xs font-medium">New password (leave empty to keep current)</label>
          <UiInput v-model="editForm.password" type="password" :placeholder="`min ${minPasswordLength} chars`" />
        </div>
        <div class="space-y-1.5">
          <label class="text-xs font-medium">Role</label>
          <UiSelect v-model="editForm.role" :options="roleOptions" aria-label="User role" />
        </div>
        <div class="space-y-1.5">
          <label class="text-xs font-medium">Status</label>
          <UiSelect
            :model-value="editForm.disabled ? 'disabled' : 'active'"
            :options="[
              { value: 'active', label: 'Active' },
              { value: 'disabled', label: 'Disabled' },
            ]"
            aria-label="Account status"
            @update:model-value="(v: string) => (editForm.disabled = v === 'disabled')"
          />
        </div>
      </form>
      <template #footer>
        <UiButton variant="outline" @click="editOpen = false">Cancel</UiButton>
        <UiButton type="submit" form="user-edit-form" :disabled="saving">
          {{ saving ? 'Saving...' : 'Save changes' }}
        </UiButton>
      </template>
    </UiDialog>

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
          <label class="text-xs font-medium">Password (min {{ minPasswordLength }} chars)</label>
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
