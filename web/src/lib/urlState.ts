export function updateQuery(params: Record<string, string | null>) {
  const q = new URLSearchParams(location.search)
  for (const [k, v] of Object.entries(params)) {
    if (v === null) q.delete(k)
    else q.set(k, v)
  }
  const qs = q.toString()
  history.replaceState(null, "", qs ? `?${qs}` : location.pathname)
}

export const readQuery = (key: string) => new URLSearchParams(location.search).get(key)
