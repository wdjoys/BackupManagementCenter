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
          <span>Dashboard</span>
        </el-menu-item>
        <el-menu-item index="/agents">
          <el-icon><Monitor /></el-icon>
          <span>Agents</span>
        </el-menu-item>
        <el-menu-item index="/storage">
          <el-icon><Folder /></el-icon>
          <span>Storage</span>
        </el-menu-item>
        <el-menu-item index="/plans">
          <el-icon><Calendar /></el-icon>
          <span>Plans</span>
        </el-menu-item>
        <el-menu-item index="/runs">
          <el-icon><List /></el-icon>
          <span>Runs</span>
        </el-menu-item>
        <el-menu-item index="/snapshots">
          <el-icon><Files /></el-icon>
          <span>Snapshots</span>
        </el-menu-item>
      </el-menu>
    </aside>

    <main class="layout-main">
      <header class="layout-header">
        <div class="user-info">
          <el-icon><User /></el-icon>
          <span>{{ authStore.me?.username || 'User' }}</span>
          <el-button text @click="handleLogout">
            <el-icon><SwitchButton /></el-icon>
            <span>Logout</span>
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
import { useAuthStore } from '@/stores/auth'
import { ElMessage } from 'element-plus'

const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()

const activeMenu = computed(() => {
  const path = route.path
  const menuMap: Record<string, string> = {
    '/dashboard': '/dashboard',
    '/agents': '/agents',
    '/storage': '/storage',
    '/plans': '/plans',
    '/runs': '/runs',
    '/snapshots': '/snapshots',
  }
  return menuMap[path] || '/dashboard'
})

async function handleLogout(): Promise<void> {
  try {
    await authStore.logout()
    ElMessage.success('Logout successful')
    router.push('/login')
  } catch (err: any) {
    ElMessage.error(err.message || 'Logout failed')
  }
}
</script>