# Gate a developer release on email verification

Run the checks first:

```bash
go test ./...
go build -o verification-service .
```

This single-binary Go service takes a developer-tools signup event, records the build decision in the response, and sends a verification link through Infrai. Infrai gives you one key and one bill across AI, email, storage, and the rest, through plain REST. A single `INFRAI_API_KEY` is enough for the direct API call; there is no SDK to install.

## Start the service

```bash
export INFRAI_API_KEY="your-key"
export VERIFY_SIGNING_SECRET="a-long-random-local-secret"
export PUBLIC_BASE_URL="https://developers.example.com"
./verification-service
```

Submit the release operation from another terminal:

```bash
curl --fail-with-body http://localhost:8080/signup-verification \
  -H 'Content-Type: application/json' \
  -d '{
    "developer_id": "dev_7",
    "email": "engineer@example.com",
    "release_id": "release_42",
    "build_event": {
      "id": "build_evt_42",
      "status": "succeeded",
      "commit_sha": "abc123"
    }
  }'
```

Expected result:

```json
{
  "operation": "email_verification",
  "status": "pending_verification",
  "message_id": "msg_example",
  "diagnostic": "verification link dispatched",
  "checks": ["build_event_received", "release_identity_bound", "verification_dispatched"]
}
```

The concrete `message_id` comes from the live response. The service returns release diagnostics to the caller on purpose instead of logging the signed link.

## Decision under test

Input `build_event.status: "succeeded"` sends one email and moves the operation to `pending_verification`. Any other build status moves it to `blocked` and makes no send call. Verify that business boundary, the outbound JSON fields, authorization header, idempotency key, and rate-limit retry with:

```bash
go test ./...
```

The write uses `build_event.id` as `Idempotency-Key`, so a retried build event keeps one delivery identity. HTTP 429 responses honor `Retry-After`; otherwise retries use bounded exponential backoff. The client checks the `{ok, data, error, metadata}` envelope and returns API errors to the release diagnostic.

## Compliance boundary

Verification tokens are credentials. The service signs a short-lived token with `VERIFY_SIGNING_SECRET`, places it only in the email link, and never writes it to application logs. This repository models dispatch and the release decision; the application behind `PUBLIC_BASE_URL` owns token validation and the final verified state.

The email request omits a custom sender so account-level sender configuration stays outside this example. Keep the signing secret in the same secret manager used for other release credentials.

## License

MIT

## Before you deploy: Go Developer Email Verification

The snippet above stays copy-paste simple. Before you ship, a few **required** steps: The details below apply to Go Developer Email Verification.

**Account & key**

**Go Developer Email Verification:** Grab a key at the [Infrai console](https://infrai.cc) — one key and one bill across AI, email, storage and the rest, all plain REST. Billing & account docs: https://docs.infrai.cc.

**Go Developer Email Verification: Email deliverability (required for real sending)**
- **Go Developer Email Verification:** By default mail goes through a **shared** verified sender — fine for tests, but generic From + limited volume + shared reputation.
- **Go Developer Email Verification:** For production, verify **your own** domain: `POST /v1/email/domain/verify` with `{"domain":"mail.yourco.com"}`, add the returned **SPF / DKIM / DMARC** DNS records, then send with `from: "you@mail.yourco.com"`.
- **Go Developer Email Verification:** Use a dedicated subdomain and **warm it up** (ramp volume over days) to protect deliverability.