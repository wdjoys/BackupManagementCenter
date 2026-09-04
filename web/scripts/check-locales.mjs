#!/usr/bin/env node
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

if (hasError) {
  process.exit(1)
}

console.log(`Locale parity check passed: ${enKeys.size} keys in sync across en-US and zh-CN.`)
