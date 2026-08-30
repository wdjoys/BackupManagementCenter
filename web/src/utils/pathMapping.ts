import type { PathMapping } from '@/api/types'

function isWindowsPath(path: string): boolean {
  return /^[a-zA-Z]:[\\/]/.test(path) || /^\\\\[^\\/]+[\\/][^\\/]+/.test(path)
}

function normalizePath(path: string): string {
  const windows = isWindowsPath(path)
  const separator = windows ? '\\' : '/'
  let normalized = windows ? path.replace(/\//g, '\\') : path.replace(/\\/g, '/')
  normalized = normalized.replace(new RegExp(`${separator === '\\' ? '\\\\' : '/'}+$`), '')
  if (!normalized) return separator
  if (/^[a-zA-Z]:$/.test(normalized)) return `${normalized}${separator}`
  return normalized
}

function comparablePath(path: string): string {
  const normalized = normalizePath(path)
  return isWindowsPath(normalized) ? normalized.toLowerCase() : normalized
}


/** Returns whether a POSIX, Windows drive, or UNC path is absolute. */
export function isAbsolutePath(path: string): boolean {
  return path.startsWith('/') || /^[a-zA-Z]:[\\/]/.test(path) || /^\\\\[^\\/]+[\\/][^\\/]+/.test(path)
}

/** Returns host mapping roots without changing their API values. */
export function hostPathRoots(mappings: readonly PathMapping[]): string[] {
  return [...new Set(mappings.map(({ host_path }) => host_path))]
}

/** Finds the longest root that contains path on a directory boundary. */
export function longestMatchingPathRoot(path: string, roots: readonly string[]): string | undefined {
  const candidate = comparablePath(path)
  let match: string | undefined
  let matchLength = -1
  for (const root of roots) {
    const normalizedRoot = comparablePath(root)
    const separator = isWindowsPath(normalizedRoot) ? '\\' : '/'
    const isMatch = normalizedRoot.endsWith(separator)
      ? candidate.startsWith(normalizedRoot)
      : candidate === normalizedRoot || candidate.startsWith(`${normalizedRoot}${separator}`)
    if (isMatch && normalizedRoot.length > matchLength) {
      match = root
      matchLength = normalizedRoot.length
    }
  }
  return match
}

/** Returns whether path belongs to one of the agent's host-path mapping roots. */
export function isWithinMappedRoot(path: string, mappings: readonly PathMapping[]): boolean {
  return mappings.length === 0 || longestMatchingPathRoot(path, hostPathRoots(mappings)) !== undefined
}
