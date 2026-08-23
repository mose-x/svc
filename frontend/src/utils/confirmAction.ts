import type { ReactNode } from 'react'
import type { Modal } from 'antd'

// Minimal static modal API shape: the first element of Modal.useModal() is
// assignable to it, which keeps this util testable with fakes.
type ModalConfirmApi = Pick<typeof Modal, 'confirm'>

export interface ConfirmActionOptions {
  modal: ModalConfirmApi
  title: ReactNode
  content?: ReactNode
  okText: string
  cancelText: string
  run: () => Promise<void>
}

// confirmAction centralizes the app-wide confirm-then-run pattern: open a
// non-mask-closable confirm dialog, switch OK to loading (cancel disabled)
// while run() is in flight, and restore the buttons when run() rejects so
// the user can retry. Callers keep their own success/error toasts inside
// run() and re-throw to keep the dialog open on failure.
export const confirmAction = ({
  modal,
  title,
  content,
  okText,
  cancelText,
  run,
}: ConfirmActionOptions) => {
  const ref = modal.confirm({
    title,
    content,
    okText,
    cancelText,
    maskClosable: false,
    onOk: async () => {
      ref.update({
        cancelButtonProps: { disabled: true },
        okButtonProps: { loading: true },
      })
      try {
        await run()
      } catch (e) {
        ref.update({
          cancelButtonProps: { disabled: false },
          okButtonProps: { loading: false },
        })
        throw e
      }
    },
  })
  return ref
}
