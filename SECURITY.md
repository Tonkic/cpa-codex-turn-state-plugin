# Security and private configuration

This plugin runs as trusted native code inside CPA and can access the selected OAuth credential through host callbacks. Review the source before installing it.

- Never commit CPA configuration, auth files, management keys, proxy URLs, state caches or request logs. Keep backups outside the repository.
- Configure proxies through private files (`url_file`) or the CPA service environment (`url_env`). Examples contain no working proxy credentials or deployment-specific endpoints.
- Restrict proxy and state files to the service account. State values are sensitive even though the plugin does not decrypt them.
- The authenticated status endpoint intentionally omits credentials, full state and proxy URLs.
- Ordinary tests use synthetic data. Do not provide production secrets to GitHub Actions, pull requests or public issue attachments.
- Release builds use `-trimpath` and stripped debug information to avoid embedding local build paths. Scan release archives as well as source before publishing.

If a credential is accidentally published, revoke or rotate it first. Deleting a file or rewriting Git history does not revoke credentials or remove third-party copies. Do not report a vulnerability by posting real credentials in a public issue.
