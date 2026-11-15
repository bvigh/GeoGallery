import { useMemo } from "react";
import { timeZoneNames } from "@/lib/timeZones";

interface Props {
  id: string;
  value: string; // an IANA zone, or "" for the default
  onChange: (zone: string) => void;
  defaultLabel: string; // the first option's text: what "" means
  disabled?: boolean;
  className?: string;
}

// A native select of IANA zones: a phone opens its own picker, and typing a name's first letters
// jumps to it. The value is always among the options, even when the browser's list lacks it.
export function CameraZoneSelect({ id, value, onChange, defaultLabel, disabled, className }: Props) {
  const names = useMemo(() => timeZoneNames(value), [value]);
  return (
    <select
      id={id}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      disabled={disabled}
      className={className}
    >
      <option value="">{defaultLabel}</option>
      {names.map((name) => (
        <option key={name} value={name}>
          {name}
        </option>
      ))}
    </select>
  );
}
