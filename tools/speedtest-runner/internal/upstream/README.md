# Adapted clash-speedtest packages

This directory contains the `speedtester` and `output` packages imported from
`github.com/faceair/clash-speedtest` v1.8.8.

Local adaptations:

- pass the Mihomo v1.19.27 tunnel argument when parsing proxy providers;
- keep proxy-provider tunnel integration disabled because this standalone
  runner does not create a Mihomo tunnel;
- remove upstream `server:port` deduplication so the runner's complete
  configuration fingerprint remains the single deduplication rule.

The upstream project license is available in the repository root `LICENSE`.
