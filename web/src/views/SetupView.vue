<template>
  <div class="auth-page">
    <div class="auth-locale-bar">
      <LocaleSwitcher />
    </div>
    <div class="auth-card">
      <h1>{{ t('auth.setupTitle') }}</h1>
      <el-form
        ref="formRef"
        :model="form"
        :rules="rules"
        label-position="top"
        @submit.prevent="handleSubmit"
      >
        <el-form-item :label="t('auth.username')" prop="username">
          <el-input v-model="form.username" :placeholder="t('auth.adminUsernamePlaceholder')" autocomplete="username" />
        </el-form-item>
        <el-form-item :label="t('auth.password')" prop="password">
          <el-input
            v-model="form.password"
            type="password"
            :placeholder="t('auth.passwordPlaceholder')"
            autocomplete="new-password"
            show-password
          />
          <div
            v-if="form.password"
            class="password-strength"
            :class="strengthClass"
          ></div>
        </el-form-item>
        <el-form-item :label="t('auth.confirmPassword')" prop="confirm">
          <el-input
            v-model="form.confirm"
            type="password"
            :placeholder="t('auth.confirmPlaceholder')"
            autocomplete="new-password"
            show-password
          />
        </el-form-item>
        <el-form-item>
          <el-button
            type="primary"
            native-type="submit"
            :loading="loading"
            style="width: 100%"
          >
            {{ t('auth.setupButton') }}
          </el-button>
        </el-form-item>
      </el-form>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import LocaleSwitcher from '@/components/LocaleSwitcher.vue'

const router = useRouter()
const authStore = useAuthStore()
const { t } = useI18n()

const formRef = ref<FormInstance>()
const loading = ref(false)

const form = ref({
  username: '',
  password: '',
  confirm: '',
})

const validateConfirm = (_rule: unknown, value: string, callback: (err?: Error) => void): void => {
  if (!value) {
    callback(new Error(t('auth.confirmationRequired')))
  } else if (value !== form.value.password) {
    callback(new Error(t('auth.passwordMismatch')))
  } else {
    callback()
  }
}

const rules = computed<FormRules>(() => ({
  username: [{ required: true, message: t('auth.usernameRequired'), trigger: 'blur' }],
  password: [
    { required: true, message: t('auth.passwordRequired'), trigger: 'blur' },
    { min: 8, message: t('auth.passwordTooShort'), trigger: 'blur' },
  ],
  confirm: [
    { required: true, message: t('auth.confirmationRequired'), trigger: 'blur' },
    { validator: validateConfirm, trigger: 'blur' },
  ],
}))

// Re-validate against the current language after a switch.
watch(() => t('auth.setupTitle'), () => {
  formRef.value?.clearValidate()
})

const strengthClass = computed(() => {
  const pw = form.value.password
  if (pw.length === 0) return ''
  const hasLetter = /[a-z]/.test(pw)
  const hasUpper = /[A-Z]/.test(pw)
  const hasDigit = /[0-9]/.test(pw)
  const hasSpecial = /[^a-zA-Z0-9]/.test(pw)
  const score = (hasLetter ? 1 : 0) + (hasUpper ? 1 : 0) + (hasDigit ? 1 : 0) + (hasSpecial ? 1 : 0)
  if (score <= 1) return 'weak'
  if (score <= 2) return 'medium'
  return 'strong'
})

async function handleSubmit(): Promise<void> {
  const valid = await formRef.value?.validate()
  if (!valid) return

  loading.value = true
  try {
    await authStore.setup(form.value.username, form.value.password)
    ElMessage.success(t('auth.setupSuccess'))
    router.push('/login')
  } catch (err: any) {
    ElMessage.error(err.message || t('auth.setupFailed'))
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.auth-page {
  flex-direction: column;
}
.auth-locale-bar {
  width: 400px;
  display: flex;
  justify-content: flex-end;
  margin-bottom: 8px;
}
</style>
