# data/

Place the source databases here:

- `dbip-city-lite.csv` — DB-IP City Lite, the seed (defines which CIDR blocks exist).
  Required.
- `GeoLite2-City.mmdb` — MaxMind GeoLite2 City. Optional but recommended.
- `IP2LOCATION-LITE-DB5.BIN` — IP2Location Lite DB5. Optional but recommended.
- `SxGeoCity.dat` — Sypex Geo. Optional; only consulted for CIS countries.

The GeoNames postal-code archive (`allCountries.zip`) is downloaded into this
directory automatically on first run by the `geozip` package.

`../scripts/download_data.sh` can download and unpack the donor databases into
the filenames expected by the builder. Tokens are read from environment
variables, not stored in the repository:

```bash
export IP2LOCATION_TOKEN='...'
export MAXMIND_ACCOUNT_ID='...'
export MAXMIND_LICENSE_KEY='...'
scripts/download_data.sh
```

Useful overrides:

- `DBIP_MONTH=2026-05`
- `DBIP_CITY_CSV_URL=https://download.db-ip.com/free/dbip-city-lite-2026-05.csv.gz`
- `IP2LOCATION_URL='https://www.ip2location.com/download?...'`
- `MAXMIND_URL='https://download.maxmind.com/geoip/databases/GeoLite2-City/download?suffix=tar.gz'`
- `FORCE_DOWNLOAD=1`
- `DOWNLOAD_ONLY=ip2location` (or `dbip`, `sypex-city`, `sypex-country`, `maxmind`)
- `DOWNLOAD_ONLY=rir` to update bulk RIR delegated stats.
- `DOWNLOAD_ONLY=geofeeds` to update allowlisted RFC 8805 geofeds from `data/geofeeds/allowlist.tsv`.
- `STATE_FILE=data/download-state.tsv`
- `GEOFEED_MAX_IPV4_BITS=24` for the builder geofeed prefix floor. The default
  ignores IPv4 entries more specific than `/24` to avoid `/32`-heavy feeds
  inflating the MMDB and overriding broad consensus with host-level records.
- `AUTO_DOWNLOAD=0` for `geo.sh` if you want to skip downloads.
- `FORCE_BUILD=1` for `geo.sh` if you want to rebuild even when all sources are unchanged.

The downloader always unpacks into a temporary file first and compares SHA256
with the currently installed database. If the content is unchanged, the old
database is kept and the temporary file is deleted. `ETag` / `Last-Modified`
headers are stored in `download-state.tsv` when the source provides them, but
checksum comparison is the final decision.

`geo.sh` reads `data/download-changed.txt`; when it is `0` and `release/geo.mmdb`
already exists, the build is skipped and the existing MMDB is kept.

Bulk non-premium sources:

- `data/rir/delegated-*-extended-latest` is downloaded from the five RIRs and
  used only for `registry_country_code`. This avoids RDAP/WHOIS rate limits.
- `data/geofeeds/allowlist.tsv` controls RFC 8805 geofeed ingestion. Only
  explicitly allowlisted URLs are downloaded; each feed is treated as an
  authoritative candidate for its prefixes, but still goes through consensus.
  Good low-cost sources are operator-published RFC 8805 feeds, OpenGeoFeed's
  public opt-in feed, GeolocateMuch's validated daily merge, and RIR WHOIS/RPSL
  `geofeed:` references discovered from bulk registry dumps. Vendor feeds such
  as Fortinet's public GeoIP CSV are useful for that vendor's own networks, but
  should not be treated as a general-purpose city database.

If only `dbip-city-lite.csv` is present, the build still succeeds — it just
runs without any consensus checks (one source = one record).
