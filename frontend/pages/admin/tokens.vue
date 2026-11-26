<script setup lang="ts">
import { Plus, Copy, Check, Ban, Trash2, KeyRound } from 'lucide-vue-next'
import type { AccessToken, PermissionScope, RegistryInstance, User } from '~/types/registry'
import { formatDate, formatRelative, copyText } from '~/utils/format'

definePageMeta({ layout: 'admin' })

const { user: currentUser } = useAuth()
const loading = ref(true)
const errorMsg = ref('')
const tokens = ref<AccessToken[]>([])
const registries = ref<RegistryInstance[]>([])
// The owner of each token, so an administrator reviewing the platform-wide list
// can tell whose token they are looking at. Tokens created before this was
// exposed have no resolvable owner and are shown as "unknown".
const owners = ref<Record<string, User>>({})
const users = ref<User[]>([])

const ownerName = (token: AccessToken) => owners.value[token.user_id]?.username || 'unknown'

// The owner of a newly created token. An administrator can issue a token on
// behalf of another account — for example to provision a CI job for a
// teammate — so the default is the administrator's own account.
const ownerOptions = computed(() => [
  ...(currentUser.value
    ? [{ value: currentUser.value.id, label: `${currentUser.value.username} (you)` }]
    : []),
  ...users.value
    .filter((u) => u.id !== currentUser.value?.id)
    .map((u) => ({ value: u.id, label: u.username })),
])

const registryScopeOptions = computed(() => [
  { value: '__all__', label: 'All Registries' },
  ...registries.value.map((r) => ({
    value: r.id,
    label: `${r.name} (/r/${r.slug} · ${r.type})`,
  })),
])

const registryScopeModel = computed({
  get: () => form.registry_id || '__all__',
  set: (v: string) => {
    form.registry_id = v === '__all__' ? '' : v
  },
})
const createOpen = ref(false)
const saving = ref(false)
const createdPlaintext = ref('')
const copiedSecret = ref(false)

const form = reactive({
  name: '',
  user_id: '',
  registry_id: '',
  scopes: ['read', 'publish'] as PermissionScope[],
  namespacesRaw: '',
  expires_days: 90,
})

const allScopes: PermissionScope[] = ['read', 'publish', 'delete', 'admin']

async function loadData() {
  loading.value = true
  errorMsg.value = ''
  try {
    const [tokRes, regRes, userRes] = await Promise.all([
      apiFetch<{ tokens: AccessToken[] }>('/api/admin/tokens'),
      apiFetch<{ registries: RegistryInstance[] }>('/api/admin/registries'),
      apiFetch<{ users: User[] }>('/api/admin/users').catch(() => ({ users: [] as User[] })),
    ])
    tokens.value = tokRes.tokens || []
    registries.value = regRes.registries || []
    const map: Record<string, User> = {}
    for (const u of userRes.users || []) map[u.id] = u
    owners.value = map
    users.value = userRes.users || []
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to load access tokens'
  } finally {
    loading.value = false
  }
}

function toggleScope(sc: PermissionScope) {
  const idx = form.scopes.indexOf(sc)
  if (idx === -1) form.scopes.push(sc)
  else form.scopes.splice(idx, 1)
}

// Opening the dialog resets the form and defaults the owner to the signed-in
// administrator, which is the common case.
function openCreate() {
  form.name = ''
  form.user_id = currentUser.value?.id || ''
  form.registry_id = ''
  form.scopes = ['read', 'publish']
  form.namespacesRaw = ''
  form.expires_days = 90
  createOpen.value = true
}

async function handleCreate() {
  saving.value = true
  errorMsg.value = ''
  try {
    const namespaces = form.namespacesRaw
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean)

    const res = await apiFetch<{ token: AccessToken; plaintext: string }>('/api/admin/tokens', {
      method: 'POST',
      body: JSON.stringify({
        name: form.name.trim(),
        user_id: form.user_id,
        registry_id: form.registry_id,
        scopes: form.scopes,
        namespaces,
        expires_days: Number(form.expires_days) || 0,
      }),
    })
    createdPlaintext.value = res.plaintext
    createOpen.value = false
    form.name = ''
    form.namespacesRaw = ''
    await loadData()
  } catch (err: any) {
    errorMsg.value = err?.message || 'Failed to create token'
  } finally {
    saving.value = false
  }
}

async function handleRevoke(tok: AccessToken) {
  await apiFetch(`/api/admin/tokens/${tok.id}/revoke`, { method: 'POST' })
  await loadData()
}

async function handleDelete(tok: AccessToken) {
  if (!confirm(`Permanently delete token "${tok.name}"?`)) return
  await apiFetch(`/api/admin/tokens/${tok.id}`, { method: 'DELETE' })
  await loadData()
}

async function copySecret() {
  if (await copyText(createdPlaintext.value)) {
    copiedSecret.value = true
    setTimeout(() => (copiedSecret.value = false), 2000)
  }
}

onMounted(loadData)
</script>

<template>
  <div class="space-y-6">
    <div class="flex flex-wrap items-center justify-between gap-4">
      <div>
        <h1 class="text-2xl font-bold tracking-tight">Access Tokens</h1>
        <p class="text-sm text-muted-foreground">
          Issue registry-scoped tokens with granular permissions and optional package namespace restrictions.
        </p>
      </div>
      <UiButton size="sm" @click="openCreate">
        <Plus class="h-4 w-4" />
        Create Token
      </UiButton>
    </div>

    <SharedErrorBanner :message="errorMsg" />

    <!-- One-time plaintext reveal banner -->
    <div
      v-if="createdPlaintext"
      class="rounded-xl border border-emerald-200 bg-emerald-50 p-5 space-y-3"
    >
      <div class="flex items-center justify-between">
        <div class="text-sm font-semibold text-emerald-900">
          Token created — copy it now! It will not be shown again.
        </div>
        <button type="button" class="text-xs text-emerald-700 underline" @click="createdPlaintext = ''">
          Dismiss
        </button>
      </div>
      <div class="flex items-center gap-2 rounded-lg bg-slate-900 px-3.5 py-2.5 text-xs font-mono text-emerald-300">
        <span class="flex-1 break-all">{{ createdPlaintext }}</span>
        <button type="button" class="shrink-0 rounded p-1 text-slate-300 hover:text-white" @click="copySecret">
          <Check v-if="copiedSecret" class="h-4 w-4 text-emerald-400" />
          <Copy v-else class="h-4 w-4" />
        </button>
      </div>
    </div>

    <UiCard class="overflow-hidden">
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="border-b bg-muted/40 text-left text-xs font-medium text-muted-foreground">
              <th class="px-5 py-3">Name &amp; Prefix</th>
              <th class="px-4 py-3">Owner</th>
              <th class="px-4 py-3">Registry Scope</th>
              <th class="px-4 py-3">Permissions</th>
              <th class="px-4 py-3">Namespaces</th>
              <th class="px-4 py-3">Status / Expiry</th>
              <th class="px-4 py-3 text-right">Actions</th>
            </tr>
          </thead>
          <tbody class="divide-y">
            <tr v-if="loading">
              <td colspan="7" class="px-5 py-8 text-center text-muted-foreground">Loading tokens...</td>
            </tr>
            <tr v-else-if="tokens.length === 0">
              <td colspan="7" class="px-5 py-10 text-center text-muted-foreground">
                No access tokens created yet.
              </td>
            </tr>
            <tr v-for="tok in tokens" :key="tok.id" class="hover:bg-muted/20">
              <td class="px-5 py-3.5">
                <div class="font-medium text-foreground flex items-center gap-1.5">
                  <KeyRound class="h-3.5 w-3.5 text-muted-foreground" />
                  {{ tok.name }}
                </div>
                <div class="font-mono text-xs text-muted-foreground">{{ tok.prefix }}••••••••</div>
              </td>
              <td class="px-4 py-3.5 text-xs">
                <span class="rounded bg-muted px-2 py-0.5 text-foreground">{{ ownerName(tok) }}</span>
              </td>
              <td class="px-4 py-3.5 text-xs">
                <span v-if="tok.registry_slug" class="font-mono rounded bg-muted px-2 py-0.5">
                  {{ tok.registry_slug }}
                </span>
                <span v-else class="text-muted-foreground">All registries</span>
              </td>
              <td class="px-4 py-3.5">
                <div class="flex flex-wrap gap-1">
                  <UiBadge v-for="sc in tok.scopes" :key="sc" variant="secondary">{{ sc }}</UiBadge>
                </div>
              </td>
              <td class="px-4 py-3.5 font-mono text-xs text-muted-foreground">
                {{ tok.namespaces?.length ? tok.namespaces.join(', ') : '*' }}
              </td>
              <td class="px-4 py-3.5 text-xs">
                <UiBadge :variant="tok.revoked ? 'destructive' : 'success'">
                  {{ tok.revoked ? 'Revoked' : 'Active' }}
                </UiBadge>
                <div class="text-[11px] text-muted-foreground mt-1">
                  {{ tok.expires_at ? `Expires ${formatDate(tok.expires_at)}` : 'No expiry' }}
                  <span v-if="tok.last_used_at"> · Used {{ formatRelative(tok.last_used_at) }}</span>
                </div>
              </td>
              <td class="px-4 py-3.5 text-right">
                <div class="inline-flex items-center gap-1">
                  <UiButton
                    v-if="!tok.revoked"
                    variant="ghost"
                    size="sm"
                    title="Revoke"
                    @click="handleRevoke(tok)"
                  >
                    <Ban class="h-3.5 w-3.5 text-amber-600" />
                  </UiButton>
                  <UiButton variant="ghost" size="sm" title="Delete" @click="handleDelete(tok)">
                    <Trash2 class="h-3.5 w-3.5 text-red-600" />
                  </UiButton>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </UiCard>

    <UiDialog :open="createOpen" @update:open="(v) => (createOpen = v)">
      <UiDialogContent class="sm:max-w-xl">
        <UiDialogHeader>
          <UiDialogTitle>Create Scoped Access Token</UiDialogTitle>
          <UiDialogDescription>
            Restrict CI/CD and developer tokens to specific registry instances, scopes, and package namespaces.
          </UiDialogDescription>
        </UiDialogHeader>

        <form id="token-form" class="space-y-4" @submit.prevent="handleCreate">
          <div class="space-y-1.5">
            <label class="text-xs font-medium">Token Name</label>
            <UiInput v-model="form.name" placeholder="e.g. flutter-ci" required />
          </div>

          <div class="space-y-1.5">
            <label class="text-xs font-medium">Token Owner</label>
            <UiSelect v-model="form.user_id">
              <UiSelectTrigger aria-label="Token owner">
                <UiSelectValue placeholder="Select owner" />
              </UiSelectTrigger>
              <UiSelectContent>
                <UiSelectItem v-for="opt in ownerOptions" :key="opt.value" :value="opt.value">
                  {{ opt.label }}
                </UiSelectItem>
              </UiSelectContent>
            </UiSelect>
            <p class="text-[11px] text-muted-foreground">
              Issue the token on behalf of another account, for example to provision a CI job.
            </p>
          </div>

          <div class="space-y-1.5">
            <label class="text-xs font-medium">Registry Scope</label>
            <UiSelect v-model="registryScopeModel">
              <UiSelectTrigger aria-label="Registry scope">
                <UiSelectValue placeholder="All Registries" />
              </UiSelectTrigger>
              <UiSelectContent>
                <UiSelectItem v-for="opt in registryScopeOptions" :key="opt.value" :value="opt.value">
                  {{ opt.label }}
                </UiSelectItem>
              </UiSelectContent>
            </UiSelect>
          </div>

        <div class="space-y-1.5">
          <label class="text-xs font-medium">Permission Scopes</label>
          <div class="flex flex-wrap gap-2">
            <button
              v-for="sc in allScopes"
              :key="sc"
              type="button"
              :class="[
                'rounded-md border px-3 py-1.5 text-xs font-medium transition-colors',
                form.scopes.includes(sc)
                  ? 'border-primary bg-primary/10 text-primary'
                  : 'border-input text-muted-foreground hover:bg-muted',
              ]"
              @click="toggleScope(sc)"
            >
              {{ sc }}
            </button>
          </div>
        </div>

        <div class="space-y-1.5">
          <label class="text-xs font-medium">Allowed Namespaces (optional, comma-separated globs)</label>
          <UiInput v-model="form.namespacesRaw" placeholder="e.g. acme_*, internal_ui" />
          <p class="text-[11px] text-muted-foreground">Leave empty to allow all packages in the scoped registry.</p>
        </div>

        <div class="space-y-1.5">
          <label class="text-xs font-medium">Expires in Days (0 = never)</label>
          <UiInput v-model="form.expires_days" type="number" min="0" />
        </div>
      </form>
        <UiDialogFooter>
          <UiButton variant="outline" @click="createOpen = false">Cancel</UiButton>
          <UiButton type="submit" form="token-form" :disabled="saving || form.scopes.length === 0">
            {{ saving ? 'Generating...' : 'Generate Token' }}
          </UiButton>
        </UiDialogFooter>
      </UiDialogContent>
    </UiDialog>
  </div>
</template>
