import type { ReactNode, SVGProps } from "react";

type IconProps = SVGProps<SVGSVGElement>;

function Icon({
  children,
  ...props
}: IconProps & { children: ReactNode }) {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 20 20"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      {...props}
    >
      {children}
    </svg>
  );
}

export function PlusIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M10 4v12M4 10h12" />
    </Icon>
  );
}

export function MenuIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M4 6h12M4 10h12M4 14h12" />
    </Icon>
  );
}

export function CloseIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="m5 5 10 10M15 5 5 15" />
    </Icon>
  );
}

export function ActivityIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M3 10h3l2-5 4 10 2-5h3" />
    </Icon>
  );
}

export function ThreadIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M4 5.5A2.5 2.5 0 0 1 6.5 3h7A2.5 2.5 0 0 1 16 5.5v5a2.5 2.5 0 0 1-2.5 2.5H9l-4 3v-3.5A2.5 2.5 0 0 1 4 10.5z" />
    </Icon>
  );
}

export function SearchIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <circle cx="8.5" cy="8.5" r="4.5" />
      <path d="m12 12 4 4" />
    </Icon>
  );
}

export function ChevronIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="m7 5 5 5-5 5" />
    </Icon>
  );
}

export function ChangesIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <circle cx="5" cy="5" r="1.5" />
      <circle cx="15" cy="15" r="1.5" />
      <path d="M5 6.5V12a3 3 0 0 0 3 3h5.5M15 13.5V8a3 3 0 0 0-3-3H6.5" />
    </Icon>
  );
}

export function FilesIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M3.5 6.5h5l1.5 2h6.5v7.5h-13zM3.5 6.5V4h5l1.5 2" />
    </Icon>
  );
}

export function TerminalIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <rect x="2.5" y="3.5" width="15" height="13" rx="2" />
      <path d="m6 8 2 2-2 2M10.5 12h3.5" />
    </Icon>
  );
}

export function PreviewIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M2.5 10s2.5-4.5 7.5-4.5 7.5 4.5 7.5 4.5-2.5 4.5-7.5 4.5S2.5 10 2.5 10Z" />
      <circle cx="10" cy="10" r="2" />
    </Icon>
  );
}

export function MoreIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <circle cx="4" cy="10" r=".8" fill="currentColor" />
      <circle cx="10" cy="10" r=".8" fill="currentColor" />
      <circle cx="16" cy="10" r=".8" fill="currentColor" />
    </Icon>
  );
}

export function WarningIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M10 3 2.5 16.5h15L10 3Z" />
      <path d="M10 8.5v3.5M10 14.5v.01" />
    </Icon>
  );
}

export function CommandIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M7.5 7.5h5v5h-5zM7.5 7.5V5.75a1.75 1.75 0 1 0-1.75 1.75zM12.5 7.5h1.75a1.75 1.75 0 1 0-1.75-1.75zM12.5 12.5v1.75a1.75 1.75 0 1 0 1.75-1.75zM7.5 12.5H5.75a1.75 1.75 0 1 0 1.75 1.75z" />
    </Icon>
  );
}
