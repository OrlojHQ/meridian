export function ProgressBar({
  value,
  label,
}: {
  value?: number;
  label: string;
}) {
  const indeterminate = value === undefined;
  return (
    <div
      className="ui-progress"
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={indeterminate ? undefined : value}
    >
      <span
        className={indeterminate ? "ui-progress-bar indeterminate" : "ui-progress-bar"}
        style={indeterminate ? undefined : { width: `${value}%` }}
      />
    </div>
  );
}
