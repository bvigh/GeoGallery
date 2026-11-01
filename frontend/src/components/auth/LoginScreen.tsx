const AUTH_ERROR_MESSAGES: Record<string, string> = {
  not_invited:
    "This account isn't invited. If you were invited, sign in with the same method (Google, Facebook, email) you used the first time.",
  invite_invalid: "This invite link is invalid, expired or has already been used. Ask for a new invite.",
  disabled: "This account has been disabled. Contact the administrator.",
};

function loginHref(params: Record<string, string | null>): string {
  const qs = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v) qs.set(k, v);
  }
  const s = qs.toString();
  return "/api/auth/login" + (s ? "?" + s : "");
}

export function LoginScreen() {
  const query = new URLSearchParams(window.location.search);
  const authError = query.get("auth_error");
  const invite = query.get("invite");
  const message = authError ? AUTH_ERROR_MESSAGES[authError] ?? "Sign-in failed. Please try again." : null;

  return (
    <div className="flex h-screen items-center justify-center bg-background p-6">
      <div className="w-full max-w-sm rounded-lg border p-6 text-center">
        <h1 className="mb-2 text-xl font-semibold tracking-tight">GeoGallery</h1>
        <p className="mb-6 text-sm text-muted-foreground">Sign in to view your gallery.</p>
        {message && (
          <p role="alert" className="mb-4 rounded-md bg-red-50 p-3 text-left text-sm text-red-700">
            {message}
          </p>
        )}
        <a
          href={loginHref({ invite })}
          className="block rounded-lg bg-primary px-4 py-2 text-sm font-medium text-primary-foreground"
        >
          Sign in
        </a>
        {authError && (
          <a
            href={loginHref({ prompt: "login", invite })}
            className="mt-2 block rounded-lg border px-4 py-2 text-sm font-medium hover:bg-accent"
          >
            Sign in with a different account
          </a>
        )}
      </div>
    </div>
  );
}
