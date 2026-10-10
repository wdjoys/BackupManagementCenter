#!/usr/bin/env node
import { readdirSync, readFileSync } from 'node:fs'
import { extname, join } from 'node:path'
import enUSModule from '../src/i18n/locales/en-US.ts'
import zhCNModule from '../src/i18n/locales/zh-CN.ts'

const enUS = enUSModule.default || enUSModule
const zhCN = zhCNModule.default || zhCNModule

function extractKeys(obj, prefix = '') {
  const keys = new Set()
  for (const [key, value] of Object.entries(obj)) {
    const fullKey = prefix ? `${prefix}.${key}` : key
    if (value && typeof value === 'object' && !Array.isArray(value)) {
      const nested = extractKeys(value, fullKey)
      for (const k of nested) {
        keys.add(k)
      }
    } else {
      keys.add(fullKey)
    }
  }
  return keys
}

const enKeys = extractKeys(enUS)
const zhKeys = extractKeys(zhCN)

const missingInZh = [...enKeys].filter((k) => !zhKeys.has(k)).sort()
const missingInEn = [...zhKeys].filter((k) => !enKeys.has(k)).sort()

let hasError = false

if (missingInZh.length > 0) {
  hasError = true
  console.error(`Missing keys in zh-CN (${missingInZh.length}):`)
  for (const key of missingInZh) {
    console.error(`  - ${key}`)
  }
}

if (missingInEn.length > 0) {
  hasError = true
  console.error(`Missing keys in en-US (${missingInEn.length}):`)
  for (const key of missingInEn) {
    console.error(`  - ${key}`)
  }
}

// 代码引用检查：t('key') 必须能在两种语言里按精确路径解析。
// parity 检查抓不到"路径写错但 key 在别的层级存在"的情况（例如代码写
// plans.rules.captureOplogRequiresAll，而 locale 里是 plans.form.*），
// 此时 i18next 会把原始 key 直接显示到界面上。
const usedKeys = new Map()
const walk = (dir) => {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, entry.name)
    if (entry.isDirectory()) {
      if (entry.name !== 'i18n' && entry.name !== 'node_modules') walk(p)
    } else if (['.ts', '.tsx'].includes(extname(entry.name))) {
      const src = readFileSync(p, 'utf8')
      for (const m of src.matchAll(/\bt\(\s*['"]([A-Za-z0-9_.]+)['"]/g)) {
        if (!usedKeys.has(m[1])) usedKeys.set(m[1], p)
      }
    }
  }
}
walk(new URL('../src/', import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1'))

const unresolved = [...usedKeys.entries()].filter(([k]) => !zhKeys.has(k) || !enKeys.has(k))
if (unresolved.length > 0) {
  hasError = true
  console.error(`Unresolved t() keys (${unresolved.length}):`)
  for (const [key, file] of unresolved) {
    console.error(`  - ${key}  (used in ${file})`)
  }
}

if (hasError) {
  process.exit(1)
}

console.log(
  `Locale parity check passed: ${enKeys.size} keys in sync across en-US and zh-CN; ${usedKeys.size} t() keys resolved.`,
)
