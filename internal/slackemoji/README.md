# Slack emoji catalog

This package embeds the standard emoji catalog Slack identifies for colon-code
message formatting and augments it at runtime with each workspace's durable
custom emoji.

The generated [catalog.json](catalog.json) is pinned to iamcal/emoji-data
revision `097705020bcf82331c9ef10df3425aad15f5043c`:

- source SHA-256:
  `1d602e65be88772bf8cc368ce16b855d719eeddbafe128d471b80203f494d29f`;
- license SHA-256:
  `ee9953a79bf2132b59b1342b217f1c377b3d03d9e7713006f6c3b89eb159f1db`;
- license: MIT, retained in [emoji-data.LICENSE](emoji-data.LICENSE).

Update intentionally rather than downloading data during a build:

```sh
./scripts/update-slack-emoji-catalog.sh
```

The updater refuses content that does not match the pinned checksums. When
moving the pin, review the upstream diff, update both checksums and revision,
regenerate the compact catalog, and run the external-contract and official-SDK
qualification gates.
