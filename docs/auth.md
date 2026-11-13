# Authentication and identity providers

GeoGallery signs users in through **one OpenID Connect (OIDC) issuer**. The issuer is set with environment variables. GeoGallery contains no vendor SDK. Social login (Google, Facebook, …) is configured **in the identity provider (IdP)**, never in GeoGallery.

- Hosted instance: **Logto Cloud** (see [Logto Cloud](#logto-cloud-step-by-step)).
- Local development and E2E: Keycloak from `docker compose --profile dev` with the dev realm in `deploy/keycloak/geogallery-realm.json`.
- Self-hosting: any standards-compliant OIDC provider. See [Other providers](#other-providers) and [Keycloak in production](#keycloak-in-production).

## How it works

- **Flow:** authorization code with PKCE (S256), `state` and `nonce`. The client is confidential: the code exchange is authenticated with `OIDC_CLIENT_SECRET`.
- **Routes:** `GET /api/auth/login` redirects to the IdP, `GET /api/auth/callback` finishes the login, `POST /api/auth/logout` ends it, and `GET /api/auth/me` returns the current user. They live under `/api`, so the Vite and nginx `/api` proxies need no extra configuration.
- **Sessions are server-side.** The browser holds only a random session cookie. Its database row stores a sha256 hash of it.
  - Cookie name: `gg_session`, or `__Host-gg_session` when `PUBLIC_URL` is `https://`.
  - Attributes: `HttpOnly`, `SameSite=Lax`, `Path=/`, and `Secure` over https.
  - Lifetime: `SESSION_TTL_HOURS` (default 720, i.e. 30 days). This is absolute: there is no sliding refresh. After it expires the user signs in again.
  - Logging out, or an admin disabling the user, revokes access immediately.
- **Identity:** a user is the pair (`OIDC_ISSUER_URL`, `sub`). The configured issuer string is stored, not the token's `iss`. `email` is for display and contact only and is never used to look users up.
- **Claims used:** `sub` (required), plus `email`, `name` and `preferred_username` for display. If the ID token carries no `email` and the IdP has a userinfo endpoint, GeoGallery reads the claims from userinfo instead, but only when its `sub` matches.
- **Invite-only.** An unknown (`issuer`, `sub`) can only become a user by signing in through a valid invite link. Without one, the login ends at `/?auth_error=not_invited`. Other errors: `invite_invalid` (expired, used or unknown invite) and `disabled`.
  - Invite links look like `{PUBLIC_URL}/api/auth/login?invite=<token>`. Each link works once.
  - `server invite [--admin] [--ttl 72h]` prints one on stdout. The Admin page creates links that are valid for 72 hours.
  - A login with an invite always sends `prompt=login` to the IdP. An existing IdP session for the wrong account can therefore not silently claim the invite.
  - The sign-in screen offers **Sign in with a different account** after an error. It sends `prompt=login`.
- **Bootstrap.** Data that existed before authentication belongs to an unclaimed **Owner** admin (`00000000-0000-0000-0000-000000000001`). `server invite --admin` creates a link that binds the next login to that Owner. Re-running the command revokes the earlier unused claim link, so a lost link is harmless. If no unclaimed admin exists, it creates a plain admin invite. No account ever becomes admin just by logging in first.
- **Startup checks.** The server exits if OIDC discovery fails, or if the discovered `issuer` differs from `OIDC_ISSUER_URL`. It also refuses to start over https while `OIDC_CLIENT_SECRET` is the dev value `geogallery-dev-secret` or the S3 credentials are `minioadmin`.

## Configuration

| Variable | Default | Notes |
|---|---|---|
| `PUBLIC_URL` | `http://localhost:5173` | The origin browsers use, `scheme://host[:port]`. One trailing `/` is trimmed. A path, query or fragment fails startup. The redirect URI, post-logout URI, CSRF trusted origin and invite links are derived from it. `https` turns on secure cookies. |
| `OIDC_ISSUER_URL` | `http://localhost:8180/realms/geogallery` | Used **verbatim**. It must equal the `issuer` in the provider's discovery document byte for byte, including any trailing slash. |
| `OIDC_DISCOVERY_URL` | = `OIDC_ISSUER_URL` | Only for the Docker/Keycloak split, where the backend reaches Keycloak as `keycloak:8080` while browsers use `localhost:8180`. For any other provider, leave it equal to the issuer. |
| `OIDC_CLIENT_ID` | `geogallery` | |
| `OIDC_CLIENT_SECRET` | `geogallery-dev-secret` | Matches the dev realm. Refused over https. |
| `OIDC_SCOPES` | `openid profile email` | Space-separated. |
| `SESSION_TTL_HOURS` | `720` | Must be positive. |

**docker compose caveat.** In `docker-compose.yml` the backend's `OIDC_DISCOVERY_URL` defaults to `http://keycloak:8080/realms/geogallery`, not to the issuer. With a managed provider, set **both** `OIDC_ISSUER_URL` and `OIDC_DISCOVERY_URL` to the same value in the root `.env`. The compose file passes only `PUBLIC_URL`, `OIDC_ISSUER_URL`, `OIDC_DISCOVERY_URL`, `OIDC_CLIENT_ID` and `OIDC_CLIENT_SECRET` to the backend container. To change `OIDC_SCOPES` or `SESSION_TTL_HOURS` under compose, add them to the backend's `environment:` block.

## Generic provider setup

Any OIDC provider works if it supports the authorization code flow with PKCE and publishes a discovery document.

1. Register a **confidential web application** (server-side or "traditional web" app) with the authorization code grant. Turn on PKCE with S256 if the provider makes it optional.
2. Set the redirect (callback) URI to `{PUBLIC_URL}/api/auth/callback`.
3. Set the post-logout redirect URI to `{PUBLIC_URL}/`, the site root.
4. Copy the issuer from `<issuer>/.well-known/openid-configuration` into `OIDC_ISSUER_URL` exactly as written. Leave `OIDC_DISCOVERY_URL` equal to it.
5. Set `OIDC_CLIENT_ID` and `OIDC_CLIENT_SECRET`.
6. Settle the final auth hostname before step 7. See [Issuer stability](#issuer-stability).
7. Run `server invite --admin`, open the printed link and sign in. This account becomes the admin and takes over any pre-existing data.
8. Optionally turn off public sign-up at the IdP. GeoGallery's invite gate already blocks uninvited accounts. With sign-up off, though, you must create each person in the IdP before they can use their GeoGallery invite.

## Logto Cloud (step by step)

This is the setup for the hosted instance.

1. **Create the application.** In the Logto Console, go to **Applications → Create application** and choose **Traditional web**.
   - Redirect URI: `{PUBLIC_URL}/api/auth/callback`
   - Post sign-out redirect URI: `{PUBLIC_URL}/`
   - Copy the **App ID** to `OIDC_CLIENT_ID` and the **App secret** to `OIDC_CLIENT_SECRET`.
2. **Choose the issuer hostname (optional custom domain).** Every tenant has a default issuer, `https://<tenant>.logto.app/oidc`, shown on the application page as **Issuer endpoint** (read-only there). You can use it as is, or serve Logto from your own subdomain, such as `auth.example.com`. Decide **before** running `server invite --admin`: users are keyed by the issuer string, so switching later means running `rebind-issuer` (see [Issuer stability](#issuer-stability)).
   - To use a custom domain, first add a DNS record at your DNS host: type **CNAME**, host = the subdomain only (e.g. `auth`), value `domains.logto.app` (use the CNAME value Logto's Domains table shows if it differs). It is a CNAME, not an IP address. The name must be a subdomain; Logto does not accept a bare domain.
   - Then add the domain in the Console under **Settings → Domains**. If the record does not resolve yet, Logto rejects it with "custom hostname does not CNAME to this zone". Check with `dig +short CNAME auth.example.com`, which must print `domains.logto.app.`
   - Wait until both DNS and SSL show as active. This usually takes minutes but can take up to 24 hours. CAA records on your domain must allow Logto's certificate authority.
   - The issuer is `https://<hostname>/oidc`, with no trailing slash. Check it against `https://<hostname>/oidc/.well-known/openid-configuration`.
   - Set `OIDC_ISSUER_URL` and `OIDC_DISCOVERY_URL` to that value.
3. **Add social connectors.** Under **Connectors → Social connectors**, add Google and Facebook with **your own** OAuth app credentials. Then enable them on the sign-in page under **Sign-in & account → Sign-up and sign-in**.
   - Google: create an OAuth client in Google Cloud and register Logto's callback URI there, as shown in the connector setup.
   - Facebook: create a Meta app. Meta requires a privacy policy URL and a data-deletion URL before the app can go live.
4. **Enable automatic account linking** under **Sign-in & account → Sign-up and sign-in**. A Google and a Facebook login with the same email then resolve to the same Logto user, and therefore the same `sub`. **Confirm that the option is available on your plan (Free) before you rely on it.** Without it, Logto asks the user whether to link or create a new account, and a new account means a new `sub`, which GeoGallery treats as a separate, uninvited person.
5. **Turn off sign-up:** **Sign-in & account → Sign-up and sign-in → Advanced options → Enable user registration** off. Consequence: an invitee with no Logto account gets "account not found" from Logto. Create the user in **User management** first, with the email they will sign in with. With automatic account linking on, their first Google or Facebook login attaches to that user.
6. Start GeoGallery with `PUBLIC_URL=https://<your-site>`, run `server invite --admin` and sign in.

Logout needs no extra step: Logto advertises `end_session_endpoint`, so logging out of GeoGallery also ends the Logto session and returns to `{PUBLIC_URL}/`.

## Issuer stability

GeoGallery identifies a user by (`issuer`, `sub`). If the issuer string changes, for example because you add a custom domain later on Logto, Auth0 or Kinde, every existing user looks unknown and is told `not_invited`.

- Pick the final auth hostname **before** `server invite --admin`.
- If it changes anyway and the provider keeps the same `sub` values:
  1. Update `OIDC_ISSUER_URL` (and `OIDC_DISCOVERY_URL`) to the new issuer.
  2. Run `server rebind-issuer --from <old issuer> --to <new issuer>`. `--from` is the previous `OIDC_ISSUER_URL` value, and `--to` must equal the new one byte for byte. The command rewrites `users.issuer` in one transaction and prints the number of rows changed. It needs only the database, not the IdP.
  3. Restart the server.
- If the new provider issues **different** `sub` values (a real provider migration), `rebind-issuer` cannot help. Re-link invites for existing users are not implemented.

## Account linking

GeoGallery does no linking itself. Each distinct `sub` is a distinct person. If someone signs in with Google once and Facebook the next time, they only reach the same library if the IdP maps both logins to one `sub`.

- **Logto:** automatic account linking by email (see above).
- **Keycloak:** the *first broker login* flow decides what happens when a brokered Google or Facebook identity's email matches an existing Keycloak user.
- **ZITADEL:** auto-linking for external IdPs, configured per identity provider.
- **Auth0 Free:** no account linking. Each social method becomes a separate GeoGallery identity.

The `not_invited` message tells users to sign in with the same method they used the first time.

## Logout

`POST /api/auth/logout` deletes the session row and clears the cookie. When discovery advertises an `end_session_endpoint`, the response sends the browser there with `id_token_hint`, `client_id` and `post_logout_redirect_uri={PUBLIC_URL}/`. The hint suppresses the "Do you want to log out?" page on Keycloak and Auth0. Otherwise the browser goes to `/`.

- The post-logout URI registered at the IdP must be `{PUBLIC_URL}/`, not the callback.
- **Auth0** tenants created before 14 November 2023 must turn on **RP-Initiated Logout End Session Endpoint Discovery** (Tenant Settings → Advanced). Otherwise discovery has no `end_session_endpoint`.
- **Google** (used directly) and **Amazon Cognito** advertise no standard `end_session_endpoint`. Logout ends only the GeoGallery session; the IdP session stays, and the next sign-in may go through without a password prompt.
- Presigned media URLs issued before logout stay valid for up to 15 minutes.

## Other providers

- **Auth0.** The issuer is `https://<tenant>.<region>.auth0.com/` (or your custom domain) **with the trailing slash**, so `OIDC_ISSUER_URL` must include it too. Configure your own Google and Facebook app keys; Auth0's dev keys are not for production. For logout and account linking, see the sections above.
- **ZITADEL.** Enable **User Info inside ID Token** on the application. Otherwise the ID token lacks `email`/`name`, and GeoGallery falls back to the userinfo endpoint on every login. ZITADEL Cloud Free offers no custom domain, so the issuer changes when you upgrade (see [Issuer stability](#issuer-stability)).
- **Google directly** (no broker). Issuer `https://accounts.google.com`. Google only, no Facebook, no account linking, and no IdP logout.
- **Facebook** cannot be used directly. Facebook Login for the web is OAuth 2.0 without an ID token, so it must sit behind a broker (Logto, Keycloak, Auth0, …). The Meta app needs privacy-policy and data-deletion URLs.
- **Supabase Auth: not supported.** Its OIDC server is beta, it requires you to build the consent UI, and free projects pause when idle.
- **Hanko: not supported.** It is not an OIDC provider (no discovery, authorization-code flow or ID token); its OIDC features make it a client of other providers.

## API tokens

Personal API tokens let scripts call the API as the user.

- Create them on the **Settings** page. The UI offers 30, 90 (default) or 365 days. The API (`POST /api/tokens`) accepts `expires_in_days` from 1 to 365, defaulting to 90. Tokens always expire.
- The plaintext (`gg_pat_` followed by 43 base64url characters) is shown **once**, at creation. Only its sha256 hash is stored.
- A user can hold at most 25 active tokens. Revoke them on the Settings page; revoked tokens stay listed as history.
- Send `Authorization: Bearer gg_pat_…`. The scheme name is case-insensitive.
  - A `Bearer` header is authoritative: an invalid, revoked or expired token returns 401, even if a valid session cookie is also present.
  - Any other scheme (for example `Basic`) is ignored, and the request falls back to the session cookie.
- Tokens act as their user, scoped to that user's data, with these exceptions. The following are **session-only** and return 403 for a token:
  - `DELETE /api/media/{id}` and `DELETE /api/trips/{id}`
  - `GET /api/media/{id}/provenance` and `GET /api/media/{id}/provenance/file` (a token cannot read location history)
  - `DELETE /api/albums/{id}`, `POST /api/albums/{id}/members`, `DELETE /api/albums/{id}/members/{user_id}` and `GET /api/users` (a token cannot widen who sees the data or list users' emails)
  - `/api/tokens*` (a token cannot mint tokens)
  - `/api/admin/*`
- Disabling a user deletes their sessions and revokes all their tokens and any unused invites they created. Re-enabling them does not bring the tokens or invites back.
- `last_used_at` is updated at most once every 5 minutes.
- Tokens are for curl and scripts. A web page on another origin cannot use them: the API sends no CORS headers, so the browser blocks such a page from reading the responses.

Example:

```bash
export GG_TOKEN=gg_pat_...        # paste it, or read it from a secrets store
curl -X POST https://<your-site>/api/gps-tracks/import \
  -H "Authorization: Bearer $GG_TOKEN" -H "Content-Type: application/json" --data @points.json
```

**Secret scanning.** The fixed prefix makes leaked tokens easy to catch. A gitleaks custom rule:

```toml
[[rules]]
id = "geogallery-pat"
description = "GeoGallery personal API token"
regex = '''gg_pat_[A-Za-z0-9_-]{43}'''
keywords = ["gg_pat_"]
```

**Reverse proxies in front of GeoGallery.**
- An HTTP Basic-auth proxy is fine: browsers keep using the session cookie, and Basic credentials are ignored.
- oauth2-proxy with `--pass-authorization-header` is **not supported**. It replaces `Authorization` with `Bearer <ID token>`, which is not a `gg_pat_` token, so every request gets 401.

## Keycloak in production

The compose `keycloak` service and `deploy/keycloak/geogallery-realm.json` are for **development and E2E only**: `start-dev`, H2 storage, admin `admin`/`admin`, test users whose password equals their username, the dev client secret and `localhost` URIs. **Never import the dev realm into a production Keycloak.**

For production:

- Run Keycloak in production mode (`kc.sh start`) with its own PostgreSQL database (`KC_DB=postgres`, …) and TLS. Set `KC_HOSTNAME` to its public https URL.
- Create a realm. Under **Realm settings → Login**, choose whether **User registration** is on:
  - On: an invitee clicks **Register** on the Keycloak login page and creates their own account. GeoGallery's invite gate still sends uninvited sign-ups to `not_invited`.
  - Off: an invitee without a Keycloak account gets "Invalid username or password." Create each person under **Users** before you send the invite.
- Create an OpenID Connect client that mirrors the dev realm's client settings:
  - **Client authentication** on (confidential client, client secret authenticator). Copy the generated secret to `OIDC_CLIENT_SECRET`.
  - **Standard flow** on; **implicit flow** and **direct access grants** off.
  - **PKCE method** `S256` (advanced setting `pkce.code.challenge.method`).
  - **Valid redirect URIs:** `{PUBLIC_URL}/api/auth/callback`.
  - **Valid post logout redirect URIs:** `{PUBLIC_URL}/`.
  - **Web origins:** empty (the browser never calls Keycloak with fetch).
  - Keep the default client scopes. Since Keycloak 25, `sub` comes from the `basic` scope, and dropping it drops `sub`.
- Configure Google and Facebook under **Identity Providers**, with your own app credentials. Use the first broker login flow to decide how they link to existing users.
- Set `OIDC_ISSUER_URL=https://<keycloak-host>/realms/<realm>` and `OIDC_DISCOVERY_URL` to the same value.

## Port hardening

`docker-compose.yml` publishes these ports on all host interfaces: PostgreSQL (`POSTGRES_PORT`, 5432), the MinIO API (`MINIO_API_PORT`, 9000), the MinIO console (`MINIO_CONSOLE_PORT`, 9001) and the backend (`SERVER_PORT`, 8080). Only the dev Keycloak is bound to `127.0.0.1`.

For a hosted instance:
- Do not expose the database, the MinIO console or the backend port publicly. Bind them to `127.0.0.1` or remove their `ports:` entries.
- Browsers reach the app only through the frontend (nginx serves the SPA and proxies `/api`), behind your TLS-terminating proxy. Its `/api/auth/` location keeps invite tokens and OIDC codes out of the access log; a directly exposed backend port bypasses that.
- Presigned media URLs are signed for `S3_PUBLIC_ENDPOINT` (default: `S3_ENDPOINT`), so that host must be reachable from browsers, and a reverse proxy in front of it must pass the `Host` header through unchanged (the signature covers it). `docker-compose.yml` sets `S3_ENDPOINT: http://minio:9000`, a container-only hostname, and `S3_PUBLIC_ENDPOINT` from `.env`, default `http://localhost:${MINIO_API_PORT:-9000}`. For a hosted instance, set `S3_PUBLIC_ENDPOINT` to a browser-reachable S3 host. The backend's own requests, including ffprobe re-reading a video for "Why here?", go to `S3_ENDPOINT`. The bucket itself is private (`mc anonymous set none`); objects are readable only through 15-minute presigned URLs.
- Replace the default `minioadmin` and database credentials. Over https, the backend refuses to start with `minioadmin`.
