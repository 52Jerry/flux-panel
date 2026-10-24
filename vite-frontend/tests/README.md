# Node Configuration Regression

Run configuration tests with `npm run test:node-config` and compile with
`npm run build`.

For browser tests, start Vite on port 3067, then run `npm run test:node-page`.
The browser test intercepts all API requests and uses synthetic authentication;
it does not read or update production nodes.

Optional environment variables:

- `TEST_BASE_URL`: local Vite URL (default `http://127.0.0.1:3067`).
- `PLAYWRIGHT_MODULE`: absolute path to an existing Playwright package.
- `BROWSER_EXECUTABLE`: installed Chrome or Edge executable.
- `SCREENSHOT_DIR`: existing output directory for desktop/mobile screenshots.

The tests cover modern port lists, legacy start/end ports, protocol settings,
network interface and listener preservation, invalid ports, null fields,
creation, responsive dialogs, and uncaught page errors.

## Compatibility

An own `port` property identifies modern nodes; legacy nodes use `portSta` and
`portEnd`. Creation follows the loaded node format. When no nodes exist, the
creation dialog offers a format selector because an empty list cannot identify
the backend contract.

Modern submissions contain `port`, `interfaceName`, `tcpListenAddr`, and
`udpListenAddr`, not legacy IP/start/end fields. Existing port lists are preserved
instead of flattened to a single range. Missing modern ports remain empty and
must be corrected before saving.

## Deployment

Deploy the complete production `dist` directory, including `index.html` and all
hashed assets, after backing up the running frontend. No database migration is
needed. Do not replace the running backend with the legacy backend in this
checkout: its node schema differs from the Hong Kong SQLite backend.

The existing local deployment script is
`local-only/scripts/deploy_flux_frontend_status_fix.py` at the workspace root.
It requires `NIUSU_HK_SSH_PASSWORD`, backs up the frontend container contents,
copies the built assets, reloads nginx, and verifies asset hashes. Updating
container files alone does not update its image; preserve the new assets in the
normal deployment configuration before recreating the container.

After deployment, verify the real authenticated node list and edit dialog,
then check a deliberately chosen safe update. Local mocked tests cannot prove
the live backend accepts every optional field. Retain the backup for rollback.
