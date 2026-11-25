import type { User } from '~/types/registry'

interface AuthMeResponse {
  authenticated: boolean
  kind: string
  is_admin: boolean
  user?: User
}

export function useAuth() {
  const user = useState<User | null>('auth_user', () => null)
  const isAdmin = useState<boolean>('auth_is_admin', () => false)
  const checked = useState<boolean>('auth_checked', () => false)

  async function refresh(force = false): Promise<boolean> {
    if (checked.value && !force) {
      return isAdmin.value
    }
    try {
      const res = await apiFetch<AuthMeResponse>('/api/admin/auth/me')
      user.value = res.user ?? null
      isAdmin.value = Boolean(res.authenticated && res.is_admin)
      checked.value = true
      return isAdmin.value
    } catch {
      user.value = null
      isAdmin.value = false
      checked.value = true
      return false
    }
  }

  async function login(username: string, password: string): Promise<void> {
    const res = await apiFetch<{ user: User }>('/api/admin/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    })
    user.value = res.user
    isAdmin.value = res.user.role === 'admin'
    checked.value = true
  }

  async function logout(): Promise<void> {
    try {
      await apiFetch('/api/admin/auth/logout', { method: 'POST' })
    } finally {
      user.value = null
      isAdmin.value = false
      checked.value = true
    }
  }

  return {
    user,
    isAdmin,
    checked,
    refresh,
    login,
    logout,
  }
}
