<template>
  <div class="auth-page">
    <div class="auth-card">
      <h1>Create Admin Account</h1>
      <el-form
        ref="formRef"
        :model="form"
        :rules="rules"
        label-position="top"
        @submit.prevent="handleSubmit"
      >
        <el-form-item label="Username" prop="username">
          <el-input v-model="form.username" placeholder="Enter admin username" autocomplete="username" />
        </el-form-item>
        <el-form-item label="Password" prop="password">
          <el-input
            v-model="form.password"
            type="password"
            placeholder="Enter password"
            autocomplete="new-password"
            show-password
          />
          <div
            v-if="form.password"
            class="password-strength"
            :class="strengthClass"
          ></div>
        </el-form-item>
        <el-form-item label="Confirm Password" prop="confirm">
          <el-input
            v-model="form.confirm"
            type="password"
            placeholder="Confirm password"
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
            Create Admin
          </el-button>
        </el-form-item>
      </el-form>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'

const router = useRouter()
const authStore = useAuthStore()

const formRef = ref<FormInstance>()
const loading = ref(false)

const form = ref({
  username: '',
  password: '',
  confirm: '',
})

const validateConfirm = (_rule: unknown, value: string, callback: (err?: Error) => void): void => {
  if (!value) {
    callback(new Error('Please confirm your password'))
  } else if (value !== form.value.password) {
    callback(new Error('Passwords do not match'))
  } else {
    callback()
  }
}

const rules: FormRules = {
  username: [{ required: true, message: 'Username is required', trigger: 'blur' }],
  password: [
    { required: true, message: 'Password is required', trigger: 'blur' },
    { min: 8, message: 'Password must be at least 8 characters', trigger: 'blur' },
  ],
  confirm: [
    { required: true, message: 'Password confirmation is required', trigger: 'blur' },
    { validator: validateConfirm, trigger: 'blur' },
  ],
}

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
    ElMessage.success('Admin account created')
    router.push('/login')
  } catch (err: any) {
    ElMessage.error(err.message || 'Setup failed')
  } finally {
    loading.value = false
  }
}
</script>