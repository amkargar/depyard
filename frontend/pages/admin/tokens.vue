<script setup lang="ts">
import { Plus, Copy, Check, Ban, Trash2, KeyRound } from 'lucide-vue-next'
import type { AccessToken, PermissionScope, RegistryInstance } from '~/types/registry'
import { formatDate, formatRelative, copyText } from '~/utils/format'

definePageMeta({ layout: 'admin' })

const loading = ref(true)
const errorMsg = ref('')
const tokens = ref<AccessToken[]>([])
const registries = ref<RegistryInstance[]>([])

const registryScopeOptions = computed(() => [
  { value: '', label: 'All Registries' },
  ...registries.value.map((r) => ({
    value: r.id,
    label: `${r.name} (/r/${r.slug} · ${r.type})`,
  })),
])
const createOpen = ref(false)
const saving = ref(false)
const createdPlaintext = ref('')
const copiedSecret = ref(false)

const form = reactive({
  name: '',
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
    const [tokRes, regRes] = await Promise.all([
      apiFetch<{ tokens: AccessToken[] }>('/api/admin/tokens'),
      apiFetch<{ registries: RegistryInstance[] }>('/api/admin/registries'),
    ])
    tokens.value = tokRes.tokens || []
    registries.value = regRes.registries || []
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
      <UiButton size="sm" @click="createOpen = true">
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
              <th class="px-4 py-3">Registry Scope</th>
              <th class="px-4 py-3">Permissions</th>
              <th class="px-4 py-3">Namespaces</th>
              <th class="px-4 py-3">Status / Expiry</th>
              <th class="px-4 py-3 text-right">Actions</th>
            </tr>
          </thead>
          <tbody class="divide-y">
            <tr v-if="loading">
              <td colspan="6" class="px-5 py-8 text-center text-muted-foreground">Loading tokens...</td>
            </tr>
            <tr v-else-if="tokens.length === 0">
              <td colspan="6" class="px-5 py-10 text-center text-muted-foreground">
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

    <UiDialog
      :open="createOpen"
      title="Create Scoped Access Token"
      description="Restrict CI/CD and developer tokens to specific registry instances, scopes, and package namespaces."
      @close="createOpen = false"
    >
      <form id="token-form" class="space-y-4" @submit.prevent="handleCreate">
        <div class="space-y-1.5">
          <label class="text-xs font-medium">Token Name</label>
          <UiInput v-model="form.name" placeholder="e.g. flutter-ci" required />
        </div>

        <div class="space-y-1.5">
          <label class="text-xs font-medium">Registry Scope</label>
          <UiSelect
            v-model="form.registry_id"
            :options="registryScopeOptions"
            aria-label="Registry scope"
          />
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
      <template #footer>
        <UiButton variant="outline" @click="createOpen = false">Cancel</UiButton>
        <UiButton type="submit" form="token-form" :disabled="saving || form.scopes.length === 0">
          {{ saving ? 'Generating...' : 'Generate Token' }}
        </UiButton>
      </template>
    </UiDialog>
  </div>
</template>
