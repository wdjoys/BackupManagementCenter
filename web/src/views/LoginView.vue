<template>
  <div class="auth-page">
    <div class="auth-locale-bar">
      <LocaleSwitcher />
    </div>
    <div class="auth-card">
      <h1>{{ t('auth.loginTitle') }}</h1>
      <el-form
        ref="formRef"
        :model="form"
        :rules="rules"
        label-position="top"
        @submit.prevent="handleSubmit"
      >
        <el-form-item :label="t('auth.username')" prop="username">
          <el-input v-model="form.username" :placeholder="t('auth.usernamePlaceholder')" autocomplete="username" />
        </el-form-item>
        <el-form-item :label="t('auth.password')" prop="password">
          <el-input
            v-model="form.password"
            type="password"
            :placeholder="t('auth.passwordPlaceholder')"
            autocomplete="current-password"
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
            {{ t('auth.loginButton') }}
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
})

const rules = computed<FormRules>(() => ({
  username: [{ required: true, message: t('auth.usernameRequired'), trigger: 'blur' }],
  password: [{ required: true, message: t('auth.passwordRequired'), trigger: 'blur' }],
}))

// Re-validate against the current language after a switch.
watch(() => t('auth.loginTitle'), () => {
  formRef.value?.clearValidate()
})

async function handleSubmit(): Promise<void> {
  const valid = await formRef.value?.validate()
  if (!valid) return

  loading.value = true
  try {
    await authStore.login(form.value.username, form.value.password)
    ElMessage.success(t('auth.loginSuccess'))
    router.push('/dashboard')
  } catch (err: any) {
    ElMessage.error(err.message || t('auth.loginFailed'))
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
