import { toast } from 'sonner'

export function toastSuccess(message: string, description?: string) {
  toast.success(message, { description })
}

export function toastError(message: string, description?: string) {
  toast.error(message, { description })
}

export function toastWarning(message: string, description?: string) {
  toast.warning(message, { description })
}

export function toastInfo(message: string, description?: string) {
  toast.info(message, { description })
}
