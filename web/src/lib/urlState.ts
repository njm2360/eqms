import { useSyncExternalStore } from "react"

type QueryPatch = Record<string, string | null>

const QUERY_EVENT = "querychange"

function buildUrl(patch: QueryPatch): string {
  const q = new URLSearchParams(location.search)
  for (const [k, v] of Object.entries(patch)) {
    if (v === null) q.delete(k)
    else q.set(k, v)
  }
  const qs = q.toString()
  return location.pathname + (qs ? `?${qs}` : "")
}

function commit(patch: QueryPatch, push: boolean) {
  const url = buildUrl(patch)
  if (url === location.pathname + location.search) return // 同一 URL を履歴に積まない
  history[push ? "pushState" : "replaceState"](null, "", url)
  window.dispatchEvent(new Event(QUERY_EVENT))
}

export const pushQuery = (patch: QueryPatch) => commit(patch, true)

export const replaceQuery = (patch: QueryPatch) => commit(patch, false)

function subscribe(cb: () => void) {
  window.addEventListener("popstate", cb)
  window.addEventListener(QUERY_EVENT, cb)
  return () => {
    window.removeEventListener("popstate", cb)
    window.removeEventListener(QUERY_EVENT, cb)
  }
}

export function useQuery(key: string): string | null {
  return useSyncExternalStore(subscribe, () => new URLSearchParams(location.search).get(key))
}
