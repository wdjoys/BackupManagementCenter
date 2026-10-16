import type { Snapshot, TreeEntry } from '@/api/types'

export type CacheStatus = 'HIT' | 'STALE' | null

export interface CachedSnapshotList {
  snapshots: Snapshot[]
  cacheStatus: CacheStatus
  verifiedAt: string | null
}

export interface CachedTree {
  entries: TreeEntry[]
  path: string
  cacheStatus: CacheStatus
  verifiedAt: string | null
}

export function normalizeSnapshotPath(p: string): string {
  if (!p || p === '/') return '/'
  const normalized = p.replace(/\/+/g, '/').replace(/\/+$/, '')
  return normalized.startsWith('/') ? normalized : `/${normalized}`
}

const listCache = new Map<string, CachedSnapshotList>()
// key: `${repoId}:${snapshotId}:${normalizedPath}`
const treeCache = new Map<string, CachedTree>()

function treeKey(repoId: string, snapshotId: string, path: string): string {
  return `${repoId}:${snapshotId}:${normalizeSnapshotPath(path)}`
}

export function getCachedSnapshotList(repoId: string): CachedSnapshotList | undefined {
  return listCache.get(repoId)
}

export function setCachedSnapshotList(repoId: string, data: CachedSnapshotList): void {
  listCache.set(repoId, data)
}

export function removeCachedSnapshotList(repoId: string): void {
  listCache.delete(repoId)
}

export function getCachedTree(repoId: string, snapshotId: string, path: string): CachedTree | undefined {
  return treeCache.get(treeKey(repoId, snapshotId, path))
}

export function setCachedTree(repoId: string, snapshotId: string, path: string, data: CachedTree): void {
  treeCache.set(treeKey(repoId, snapshotId, path), data)
}

export function removeCachedTree(repoId: string, snapshotId: string, path: string): void {
  treeCache.delete(treeKey(repoId, snapshotId, path))
}

export function removeSnapshotFromCache(repoId: string, snapshotId: string): void {
  const currentList = listCache.get(repoId)
  if (currentList) {
    listCache.set(repoId, {
      ...currentList,
      snapshots: currentList.snapshots.filter((s) => s.id !== snapshotId),
    })
  }
  const prefix = `${repoId}:${snapshotId}:`
  for (const key of Array.from(treeCache.keys())) {
    if (key.startsWith(prefix)) {
      treeCache.delete(key)
    }
  }
}

export function reconcileCachedTrees(repoId: string, validSnapshotIds: Set<string>): void {
  const repoPrefix = `${repoId}:`
  for (const key of Array.from(treeCache.keys())) {
    if (key.startsWith(repoPrefix)) {
      const parts = key.split(':')
      const snapId = parts[1]
      if (snapId && !validSnapshotIds.has(snapId)) {
        treeCache.delete(key)
      }
    }
  }
}

export function clearBrowseCache(): void {
  listCache.clear()
  treeCache.clear()
}
