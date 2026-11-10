import type { ButtonHTMLAttributes } from "react";

type ButtonVariant = "default" | "primary" | "secondary" | "danger";

const variantClass: Record<ButtonVariant, string> = {
  default: "",
  primary: "primary-button",
  secondary: "secondary",
  danger: "danger",
};

export function Button({
  variant = "default",
  className,
  type = "button",
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant }) {
  const classes = ["ui-button", variantClass[variant], className].filter(Boolean).join(" ");
  return <button type={type} className={classes} {...props} />;
}
