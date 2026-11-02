import { ApiError } from "@/api/client";

// Says why an album call failed when the status tells: 404 means the caller
// cannot see the album (or the target), 403 an owner-only action.
export function describeError(
  action: string,
  err: unknown,
  notFound = "the album no longer exists, or you are no longer a member"
): string {
  if (err instanceof ApiError && err.status === 403) return `${action}: only the album owner can do that`;
  if (err instanceof ApiError && err.status === 404) return `${action}: ${notFound}`;
  return action;
}
