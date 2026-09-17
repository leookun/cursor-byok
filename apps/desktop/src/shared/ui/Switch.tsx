import type { ButtonHTMLAttributes } from "react";
import styles from "./Switch.module.scss";

type SwitchProps = {
  checked: boolean;
  label: string;
  onChange: (checked: boolean) => void;
  /** small 用于行内紧凑位置（如分组标题），默认尺寸用于表单。 */
  size?: "small";
} & Omit<ButtonHTMLAttributes<HTMLButtonElement>, "aria-label" | "children" | "onChange" | "role">;

export function Switch({ checked, disabled, label, onChange, onClick, size, ...props }: SwitchProps) {
  return <button
    {...props}
    type="button"
    role="switch"
    aria-checked={checked}
    aria-label={label}
    disabled={disabled}
    className={[styles.root, size === "small" && styles.small, props.className].filter(Boolean).join(" ")}
    data-checked={checked || undefined}
    onClick={(event) => {
      onClick?.(event);
      if (!event.defaultPrevented) onChange(!checked);
    }}
  ><span /></button>;
}
