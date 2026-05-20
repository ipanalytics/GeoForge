# Third-party data and publication notes

This repository contains code for building a derived GeoIP database. It should
not publish downloaded source databases, generated release files, or private
download credentials.

## Do not commit

- `admin.env`
- downloaded files under `data/`
- generated files under `release/`
- API tokens, account IDs, license keys, or authenticated download URLs

The repository includes `admin.env.example` as a safe template.

## Data sources

Review the current terms of each source before using the downloader or
publishing derived outputs:

- MaxMind GeoLite2: requires accepting MaxMind's GeoLite EULA. Sharing derived
  data may require attribution and, for redistribution use cases, a commercial
  redistribution license.
- IP2Location LITE: free for internal use under their LITE terms. External
  redistribution or SaaS use may require a redistribution license.
- DB-IP Lite: DB-IP states that free downloads are under a Creative Commons
  license and can be used commercially/redistributed if the license terms are
  followed.
- GeoNames: data is published under Creative Commons Attribution.
- Sypex Geo: the public documentation says the format can be used in free and
  commercial products, but database update subscription and redistribution terms
  should be checked before publishing derived data.
- RFC 8805 / RFC 9632 geofeeds: operator-published CSV feeds are intended for
  geolocation correction. Treat each feed as third-party data and keep a
  documented allowlist.
- RIR delegated stats: use the public delegated statistics only for registry
  metadata unless the applicable RIR terms allow broader redistribution.

## Recommended public repository scope

Safe to publish:

- Go source code
- shell scripts
- README files
- `admin.env.example`
- `data/README.md`
- `data/geofeeds/allowlist.tsv`

Do not publish without legal review:

- `release/geo.mmdb`
- `release/geo.csv`
- raw downloaded source databases
- merged/derived databases built from licensed vendors

If you want to distribute a generated database, maintain a separate attribution
file and confirm that every included source permits redistribution for your use
case.
