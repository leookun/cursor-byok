import { useId, type ReactNode } from "react";
import { Modal } from "./Modal";

export function ConfirmDialog({ id, open, title, children, busy, wide, cancelLabel = t("取消"), confirmLabel = t("确认"), onCancel, onConfirm, destructive = false, confirmDisabled = false }: {
  id?: string;
  open: boolean;
  title: string;
  children: ReactNode;
  busy?: boolean;
  destructive?: boolean;
  confirmDisabled?: boolean;
  wide?: boolean;
  cancelLabel?: string;
  confirmLabel?: string;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const contentId = useId();
  return <Modal
    id={id}
    open={open}
    title={title}
    busy={busy}
    destructive={destructive}
    submitDisabled={confirmDisabled}
    wide={wide}
    role="alertdialog"
    ariaDescribedBy={contentId}
    initialFocus="submit"
    closeLabel={cancelLabel}
    submitLabel={confirmLabel}
    onClose={onCancel}
    onSubmit={onConfirm}
  >
    <div id={contentId}>{children}</div>
  </Modal>;
}
