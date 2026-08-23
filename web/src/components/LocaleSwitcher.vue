<template>
  <el-dropdown trigger="click" @command="handleCommand">
    <span class="locale-switcher" :class="{ compact: props.compact }">
      <span>{{ currentLabel }}</span>
      <el-icon class="el-icon--right"><ArrowDown /></el-icon>
    </span>
    <template #dropdown>
      <el-dropdown-menu>
        <el-dropdown-item
          v-for="option in options"
          :key="option.value"
          :command="option.value"
          :disabled="option.value === locale"
        >
          {{ option.label }}
        </el-dropdown-item>
      </el-dropdown-menu>
    </template>
  </el-dropdown>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowDown } from '@element-plus/icons-vue'
import { setLocale, type SupportedLocale } from '@/i18n'

const props = defineProps<{
  compact?: boolean
}>()

const { locale } = useI18n()

// Language names are shown in their own language, never translated.
const options: Array<{ value: SupportedLocale; label: string }> = [
  { value: 'zh-CN', label: '简体中文' },
  { value: 'en-US', label: 'English' },
]

const currentLabel = computed(
  () => options.find((o) => o.value === locale.value)?.label ?? locale.value,
)

function handleCommand(command: SupportedLocale): void {
  setLocale(command)
}
</script>

<style scoped>
.locale-switcher {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  cursor: pointer;
  color: var(--el-text-color-regular);
  font-size: 14px;
  outline: none;
}
.locale-switcher.compact {
  font-size: 13px;
}
.locale-switcher:hover {
  color: var(--el-color-primary);
}
</style>
