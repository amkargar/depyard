export class ApiError extends Error {
  status: number
  constructor(message: string, status: number) {
    super(message)
    this.status = status
  }
}

export async function apiFetch<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers || {})
  if (options.body && !headers.has('Content-Type') && !(options.body instanceof FormData)) {
    headers.set('Content-Type', 'application/json')
  }

  const res = await fetch(path, {
    ...options,
    headers,
    credentials: 'include',
  })

  if (!res.ok) {
    let msg = `Request failed (${res.status})`
    try {
      const data = await res.json()
      if (typeof data?.error === 'string') msg = data.error
      else if (data?.error?.message) msg = data.error.message
    } catch {
      // ignore non-json error body
    }
    throw new ApiError(msg, res.status)
  }

  if (res.status === 204) {
    return undefined as T
  }
  return (await res.json()) as T
}
