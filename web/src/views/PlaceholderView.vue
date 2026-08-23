<template>
  <div class="placeholder-page">
    <div class="placeholder-icon">🚧</div>
    <h2>{{ pageTitle }}</h2>
    <p>{{ t('placeholder.description') }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'

const route = useRoute()
const { t } = useI18n()

const pageTitleKeysMap = {
  Storage: 'placeholder.storageTargets',
  Plans: 'placeholder.backupPlans',
  Runs: 'placeholder.runHistory',
  Snapshots: 'placeholder.snapshotsRestore',
} as const

type PageName = keyof typeof pageTitleKeysMap

const pageTitle = computed(() => {
  const name = route.name as PageName | undefined
  return name && name in pageTitleKeysMap
    ? t(pageTitleKeysMap[name])
    : t('placeholder.underConstruction')
})
</script>
