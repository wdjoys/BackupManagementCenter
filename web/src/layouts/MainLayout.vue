<template>
  <div class="layout-container">
    <aside class="layout-sidebar">
      <div class="logo">
        <span>BMC</span>
      </div>
      <el-menu
        :default-active="activeMenu"
        router
        background-color="transparent"
        text-color="#bfcbd9"
        active-text-color="#409eff"
      >
        <el-menu-item index="/dashboard">
          <el-icon><Odometer /></el-icon>
          <span>{{ t('nav.dashboard') }}</span>
        </el-menu-item>
        <el-menu-item index="/agents">
          <el-icon><Monitor /></el-icon>
          <span>{{ t('nav.agents') }}</span>
        </el-menu-item>
        <el-menu-item index="/logs">
          <el-icon><Document /></el-icon>
          <span>{{ t('nav.logs') }}</span>
        </el-menu-item>
        <el-menu-item index="/storage">
          <el-icon><Folder /></el-icon>
          <span>{{ t('nav.storage') }}</span>
        </el-menu-item>
        <el-menu-item index="/plans">
          <el-icon><Calendar /></el-icon>
          <span>{{ t('nav.plans') }}</span>
        </el-menu-item>
        <el-menu-item index="/runs">
          <el-icon><List /></el-icon>
          <span>{{ t('nav.runs') }}</span>
        </el-menu-item>
        <el-menu-item index="/snapshots">
          <el-icon><Files /></el-icon>
          <span>{{ t('nav.snapshots') }}</span>
        </el-menu-item>
        <el-menu-item index="/settings">
          <el-icon><Setting /></el-icon>
          <span>{{ t('nav.settings') }}</span>
        </el-menu-item>
      </el-menu>
    </aside>

    <main class="layout-main">
      <header class="layout-header">
        <div class="user-info">
          <LocaleSwitcher />
          <el-icon><User /></el-icon>
          <span>{{ authStore.me?.username || t('layout.defaultUser') }}</span>
          <el-button text @click="handleLogout">
            <el-icon><SwitchButton /></el-icon>
            <span>{{ t('layout.logout') }}</span>
          </el-button>
        </div>
      </header>
      <div class="layout-content">
        <router-view />
      </div>
    </main>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { ElMessage } from 'element-plus'
import LocaleSwitcher from '@/components/LocaleSwitcher.vue'

const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()
const { t } = useI18n()

const activeMenu = computed(() => {
  const path = route.path
  const menuMap: Record<string, string> = {
    '/dashboard': '/dashboard',
    '/agents': '/agents',
    '/logs': '/logs',
    '/plans': '/plans',
    '/runs': '/runs',
    '/snapshots': '/snapshots',
    '/settings': '/settings',
  }
  return menuMap[path] || '/dashboard'
})

async function handleLogout(): Promise<void> {
  try {
    await authStore.logout()
    ElMessage.success(t('layout.logoutSuccess'))
    router.push('/login')
  } catch (err: any) {
    ElMessage.error(err.message || t('layout.logoutFailed'))
  }
}
</script>
