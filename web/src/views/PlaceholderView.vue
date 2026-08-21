<template>
  <div class="placeholder-page">
    <div class="placeholder-icon">🚧</div>
    <h2>{{ pageTitle }}</h2>
    <p>This page is under construction. It will be available in a future release.</p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'

const route = useRoute()

const pageTitlesMap = {
  Storage: 'Storage Targets',
  Plans: 'Backup Plans',
  Runs: 'Run History',
  Snapshots: 'Snapshots & Restore',
} as const

type PageName = keyof typeof pageTitlesMap

const pageName = computed(() => {
  const name = route.name as PageName | undefined
  return name || ''
})

const pageTitle = computed(() => {
  const n = pageName.value
  return n && n in pageTitlesMap ? pageTitlesMap[n] : 'Under Construction'
})
</script>