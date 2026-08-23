<template>
  <div class="tag-input">
    <div v-if="(props.modelValue?.length ?? 0) > 0" class="tag-list">
      <el-tag v-for="(item, index) in props.modelValue" :key="`${index}-${item}`" closable size="small" @close="removeAt(index)">
        {{ item }}
      </el-tag>
    </div>
    <el-input
      v-model="draft"
      size="small"
      :placeholder="placeholderText"
      @keydown.enter.prevent="add"
      @blur="add"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
  modelValue?: string[]
  placeholder?: string
}>()

const { t } = useI18n()

const placeholderText = computed(() => props.placeholder ?? t('common.tagInputPlaceholder'))

const emit = defineEmits<{
  (e: 'update:modelValue', value: string[]): void
}>()

const draft = ref('')

function add(): void {
  const value = draft.value.trim()
  draft.value = ''
  if (!value) return
  if (props.modelValue?.includes(value)) return
  emit('update:modelValue', [...(props.modelValue ?? []), value])
}

function removeAt(index: number): void {
  const next = [...(props.modelValue ?? [])]
  next.splice(index, 1)
  emit('update:modelValue', next)
}
</script>

<style scoped>
.tag-input {
  width: 100%;
}
.tag-list {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-bottom: 8px;
}
</style>
