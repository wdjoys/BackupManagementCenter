import React from 'react'
import { useTranslation } from 'react-i18next'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

/** 后端契约以字节为单位（estimate × 1.3 做临时空间预检，100 GiB 为硬上限）。
    表单按 GiB 输入，避免用户直接填 10 位裸数字。 */
export const BYTES_PER_GIB = 1024 ** 3

/** 字节 → GiB，保留一位小数，与输入框精度一致；避免浮点噪声写回输入框。 */
export function bytesToGiB(bytes?: number | null): number | undefined {
  if (bytes == null || bytes <= 0) return undefined
  return Number((bytes / BYTES_PER_GIB).toFixed(1))
}

/** GiB → 字节，四舍五入到整数，与后端 int64 字段对齐。 */
export function gibToBytes(gib?: number | null): number | undefined {
  if (gib == null || gib <= 0) return undefined
  return Math.round(gib * BYTES_PER_GIB)
}

export interface DumpSizeInputProps {
  id: string
  required?: boolean
  /** 字节值（表单模型保持后端单位）。 */
  value?: number | null
  /** 回调收到的同样是字节值。 */
  onChange: (bytes: number | undefined) => void
  error?: string
  submitting: boolean
}

export const DumpSizeInput: React.FC<DumpSizeInputProps> = ({
  id,
  required,
  value,
  onChange,
  error,
  submitting,
}) => {
  const { t } = useTranslation()

  // 输入框持有本地文本态。若直接把数值转回字符串做受控值，用户输入 "0" 或
  // "0." 这类中间态会被立即规整掉（0 被吃掉、小数点跑到最前），无法输入小数。
  const [text, setText] = React.useState(() => {
    const g = bytesToGiB(value)
    return g == null ? '' : String(g)
  })

  // 外部值变化（切换 kind、编辑回填、重置）时同步文本；用户输入过程中
  // 父子值一致，不会打断键入。
  const lastSyncedRef = React.useRef(value)
  React.useEffect(() => {
    if (lastSyncedRef.current === value) return
    lastSyncedRef.current = value
    const g = bytesToGiB(value)
    setText(g == null ? '' : String(g))
  }, [value])

  const handleChange = (raw: string) => {
    setText(raw)
    const parsed = Number(raw)
    // 仅当文本构成有效正数时向上同步；"0" / "0." / "." 等中间态保持不提交，
    // 由 PlanForm 的必填校验在提交时兜底。
    onChange(Number.isFinite(parsed) && parsed > 0 ? gibToBytes(parsed) : undefined)
  }

  return (
    <div className="space-y-1.5">
      <Label htmlFor={id} className="text-xs">
        {t('plans.form.estimatedDumpSize')} {required && '*'}
      </Label>
      <div className="relative">
        <Input
          id={id}
          type="number"
          // min 必须与 step 的基准对齐：min=0.1 配 step=0.5 会让合法值变成
          // 0.1+n×0.5（1.6/2.1…），用户输入 2 会被原生校验拒绝。
          min={0}
          max={100}
          step={0.1}
          inputMode="decimal"
          value={text}
          onChange={(e) => handleChange(e.target.value)}
          disabled={submitting}
          className="h-9 pr-12 text-xs"
        />
        <span className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-xs text-muted-foreground">
          {t('plans.form.dumpSizeUnit')}
        </span>
      </div>
      {error && <p className="text-[11px] text-destructive">{error}</p>}
      <p className="text-xs text-muted-foreground">{t('plans.form.dumpBytesHint')}</p>
    </div>
  )
}
