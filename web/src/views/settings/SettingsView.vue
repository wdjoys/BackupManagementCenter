<template>
  <div>
    <div class="section-title">{{ t('settings.title') }}</div>

    <el-card style="max-width: 640px">
      <template #header>
        <div style="display: flex; align-items: center; gap: 8px">
          <el-icon><Bell /></el-icon>
          <span>{{ t('settings.telegram.title') }}</span>
        </div>
      </template>

      <el-alert
        v-if="configured"
        type="success"
        :title="t('settings.telegram.configured')"
        :description="t('settings.telegram.currentTarget', { chatId })"
        show-icon
        :closable="false"
        style="margin-bottom: 16px"
      />
      <el-alert
        v-else
        type="info"
        :title="t('settings.telegram.notConfigured')"
        show-icon
        :closable="false"
        style="margin-bottom: 16px"
      />

      <el-form :model="form" label-width="auto" @submit.prevent>
        <el-form-item :label="t('settings.telegram.botToken')">
          <el-input
            v-model="form.bot_token"
            type="password"
            show-password
            :placeholder="
              configured ? t('settings.telegram.tokenPlaceholderConfigured') : '123456789:AA...'
            "
            autocomplete="new-password"
          />
        </el-form-item>
        <el-form-item :label="t('settings.telegram.chatId')">
          <el-input
            v-model="form.chat_id"
            :placeholder="configured ? chatId : '-1001234567890'"
          />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="saving" @click="handleSave">
            {{ t('settings.telegram.save') }}
          </el-button>
          <el-button :loading="clearing" :disabled="!configured" @click="handleClear">
            {{ t('settings.telegram.clear') }}
          </el-button>
        </el-form-item>
      </el-form>

      <el-divider style="margin: 8px 0" />
      <div class="settings-help">
        {{ t('settings.telegram.help') }}
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import { apiGet, apiPut } from '@/api/client'
import type { TelegramSettings, TelegramSettingsUpdate } from '@/api/types'

const { t } = useI18n()

const configured = ref(false)
const chatId = ref('')
const saving = ref(false)
const clearing = ref(false)
const form = ref<TelegramSettingsUpdate>({ bot_token: '', chat_id: '' })

async function loadSettings(): Promise<void> {
  try {
    const s = await apiGet<TelegramSettings>('/settings/telegram')
    configured.value = s.configured
    chatId.value = s.chat_id || ''
  } catch (err: any) {
    ElMessage.error(err.message || t('common.error'))
  }
}

async function handleSave(): Promise<void> {
  if (!form.value.bot_token.trim() || !form.value.chat_id.trim()) {
    ElMessage.warning(t('settings.telegram.pairRequired'))
    return
  }
  saving.value = true
  try {
    const s = await apiPut<TelegramSettings>('/settings/telegram', {
      bot_token: form.value.bot_token.trim(),
      chat_id: form.value.chat_id.trim(),
    })
    configured.value = s.configured
    chatId.value = s.chat_id || ''
    form.value.bot_token = ''
    ElMessage.success(t('settings.telegram.saved'))
  } catch (err: any) {
    ElMessage.error(err.message || t('common.error'))
  } finally {
    saving.value = false
  }
}

async function handleClear(): Promise<void> {
  clearing.value = true
  try {
    await apiPut<TelegramSettings>('/settings/telegram', { bot_token: '', chat_id: '' })
    configured.value = false
    chatId.value = ''
    form.value.bot_token = ''
    form.value.chat_id = ''
    ElMessage.success(t('settings.telegram.cleared'))
  } catch (err: any) {
    ElMessage.error(err.message || t('common.error'))
  } finally {
    clearing.value = false
  }
}

onMounted(loadSettings)
</script>

<style scoped>
.settings-help {
  color: var(--el-text-color-secondary);
  font-size: 13px;
  line-height: 1.6;
}
</style>
