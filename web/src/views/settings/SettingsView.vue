<template>
  <div class="settings-view">
    <div class="section-title">{{ t('settings.title') }}</div>
    <section class="telegram-card">
      <h2>{{ t('settings.telegram.title') }}</h2>
      <p>{{ configured ? t('settings.telegram.currentTarget', { chatId }) : t('settings.telegram.notConfigured') }}</p>
      <label>
        <span>{{ t('settings.telegram.botToken') }}</span>
        <input v-model="botToken" type="password" placeholder="123456789:AA..." autocomplete="new-password" />
      </label>
      <label>
        <span>{{ t('settings.telegram.chatId') }}</span>
        <input v-model="chatId" placeholder="-1001234567890" />
      </label>
      <p class="error">{{ error }}</p>
      <button type="button" :disabled="saving" @click="save">{{ saving ? t('common.loading') : t('settings.telegram.save') }}</button>
      <button type="button" :disabled="clearing" @click="clear">{{ clearing ? t('common.loading') : t('settings.telegram.clear') }}</button>
      <p class="settings-help">{{ t('settings.telegram.help') }}</p>
    </section>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()
const botToken = ref('')
const chatId = ref('')
const configured = ref(false)
const saving = ref(false)
const clearing = ref(false)
const error = ref('')

async function updateSettings(token: string, chat: string): Promise<void> {
  const csrf = document.cookie
    .split('; ')
    .find((entry) => entry.startsWith('bmc_csrf='))
    ?.slice('bmc_csrf='.length) || ''
  const response = await fetch('/api/v1/settings/telegram', {
    method: 'PUT',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': decodeURIComponent(csrf) },
    body: JSON.stringify({ bot_token: token, chat_id: chat }),
  })
  const payload = await response.json().catch(() => null)
  if (!response.ok) throw new Error(payload?.error?.message || response.statusText)
  configured.value = payload.configured
  chatId.value = payload.chat_id || ''
}

async function save(): Promise<void> {
  error.value = ''
  if (!botToken.value.trim() || !chatId.value.trim()) {
    error.value = t('settings.telegram.pairRequired')
    return
  }
  saving.value = true
  try {
    await updateSettings(botToken.value.trim(), chatId.value.trim())
    botToken.value = ''
  } catch (err: any) {
    error.value = err.message || t('common.error')
  } finally {
    saving.value = false
  }
}

async function clear(): Promise<void> {
  error.value = ''
  clearing.value = true
  try {
    await updateSettings('', '')
    botToken.value = ''
    chatId.value = ''
  } catch (err: any) {
    error.value = err.message || t('common.error')
  } finally {
    clearing.value = false
  }
}
</script>

<style scoped>
.telegram-card { max-width: 640px; padding: 20px; border: 1px solid var(--el-border-color-light); border-radius: 4px; background: var(--el-bg-color); }
.telegram-card h2 { margin: 0 0 16px; font-size: 16px; }
label { display: block; margin: 16px 0; }
label span { display: block; margin-bottom: 6px; }
input { box-sizing: border-box; width: 100%; padding: 8px; border: 1px solid var(--el-border-color); border-radius: 4px; }
button { margin-right: 8px; padding: 8px 15px; border: 1px solid var(--el-border-color); border-radius: 4px; background: var(--el-fill-color-blank); cursor: pointer; }
button:disabled { cursor: not-allowed; opacity: .6; }
.error { min-height: 1.5em; color: var(--el-color-danger); }
.settings-help { color: var(--el-text-color-secondary); line-height: 1.6; }
</style>
