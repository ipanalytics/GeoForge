# GeoForge

GeoForge compiles a local IPv4 GeoIP database from multiple free or low-cost
data sources. The builder uses DB-IP Lite as the prefix seed, merges location
candidates from MaxMind GeoLite2, IP2Location LITE, Sypex Geo, and allowlisted
operator geofeeds, then enriches the result with GeoNames postal and city
reference data.

The output is a MaxMind DB (`release/geo.mmdb`) plus a CSV audit copy
(`release/geo.csv`) used by the quality checker.

## Repository Layout

```text
cmd/
  builder/       Main database compiler.
  qualitycheck/  Post-build quality report.
internal/
  consensus/     Candidate scoring and merge logic.
  geofeed/       RFC 8805 geofeed parser and lookup index.
  geozip/        GeoNames postal/city indexes and coordinate normalization.
  output/        Final output cleanup.
  refdata/       Country, currency, calling code, EU, and source priority data.
  rirstats/      Bulk RIR delegated-statistics index.
  strnorm/       Name and postal normalization utilities.
data/
  Source databases and downloader state. Generated data is gitignored.
release/
  Build outputs. Generated files are gitignored.
scripts/
  Data download automation.
```

## Data Sources

The builder can use these sources when present:

| Source | Role |
| --- | --- |
| DB-IP City Lite CSV | Required prefix seed. Defines the blocks to write. |
| MaxMind GeoLite2 City | Optional consensus input. Strong global baseline. |
| IP2Location LITE DB5 | Optional consensus input. Useful independent city/postal signal. |
| Sypex Geo City | Optional consensus input for CIS countries. |
| RFC 8805 geofeeds | Optional operator-published corrections from `data/geofeeds/allowlist.tsv`. |
| RIR delegated stats | Registry country signal for `registry_country_code`. |
| GeoNames postal dump | Postal/city enrichment and public coordinate reference. |
| GeoNames cities1000 dump | `city_geoname_id` enrichment. |

Downloaded source files and generated release files are not intended to be
committed. See [THIRD_PARTY_DATA.md](THIRD_PARTY_DATA.md) before publishing or
redistributing any derived database.

## Build

Create a local `admin.env` from the template and fill only the credentials you
actually have:

```bash
cp admin.env.example admin.env
```

Run the build:

```bash
./geo.sh
```

The script:

1. Acquires a build lock under `release/`.
2. Downloads and updates source databases when they changed.
3. Skips the MMDB build if no source changed and an existing release is present.
4. Runs Go tests.
5. Compiles `./builder`.
6. Builds `release/geo.mmdb` and `release/geo.csv`.
7. Runs the post-build quality check.

Use `FORCE_BUILD=1 ./geo.sh` to rebuild even when sources are unchanged.
Use `AUTO_DOWNLOAD=0 ./geo.sh` to build from the currently installed data files.

## Update Semantics

`scripts/download_data.sh` downloads into temporary files, unpacks archives when
needed, computes SHA256 of the final unpacked file, and replaces the installed
source only when content changed. The visible state files are:

- `data/download-state.tsv`
- `data/download-changed.txt`

When `data/download-changed.txt` is `0`, `geo.sh` keeps the existing MMDB unless
`FORCE_BUILD=1` is set.

## Output Schema

Each MMDB record contains top-level metadata and a nested `location` object.

Top-level fields:

| Field | Type | Description |
| --- | --- | --- |
| `matched_prefix` | String | CIDR written to the MMDB. |
| `confidence` | Uint16 | Merge confidence, 0-100. |
| `source_updated_at` | String | UTC build timestamp. |
| `country_metadata` | Map | Country-level calling code, currency, and EU metadata. |
| `location` | Map | Geo fields listed below. |

`location` fields:

| Field | Type | Description |
| --- | --- | --- |
| `continent_code` | String | Continent code. |
| `continent_name` | String | Continent name. |
| `country_code` | String | Geo country ISO 3166-1 alpha-2. |
| `country_name` | String | Short English country name. |
| `registry_country_code` | String | Registry country from RIR delegated stats when available. |
| `subdivision_name` | String | Cleaned state/region/admin name. |
| `city_geoname_id` | Uint32 | GeoNames city id when resolved. |
| `city_name` | String | Cleaned display city name. |
| `postal_code` | String | Consensus or GeoNames-derived postal code. |
| `latitude` | Float64 | Final latitude rounded to five decimals. |
| `longitude` | Float64 | Final longitude rounded to five decimals. |
| `time_zone` | String | Computed from final coordinates. |
| `accuracy_radius_km` | Uint16 | Conservative radius derived from confidence and prefix size. |

`calling_code`, `currency_code`, and `is_eu_member` are kept under
`country_metadata`. DST status is intentionally not stored; consumers should
compute it from `time_zone` at request time.

## Example JSON

```json
{
  "ip": "1.208.10.20",
  "matched_prefix": "1.208.0.0/12",
  "confidence": 85,
  "source_updated_at": "2026-05-20T00:00:00Z",
  "location": {
    "continent_code": "AS",
    "continent_name": "Asia",
    "country_code": "KR",
    "country_name": "South Korea",
    "registry_country_code": "KR",
    "subdivision_name": "",
    "city_geoname_id": 1835848,
    "city_name": "Seoul",
    "postal_code": "04524",
    "latitude": 37.56631,
    "longitude": 126.9772,
    "time_zone": "Asia/Seoul",
    "accuracy_radius_km": 20
  },
  "country_metadata": {
    "KR": {
      "country_name": "South Korea",
      "calling_code": "82",
      "currency_code": "KRW",
      "is_eu_member": false
    }
  }
}
```

## Output Quality

The generated database is designed to be materially better than any single free
or lite source for operational use, because it combines independent signals and
rejects or downranks obvious bad records before writing output.

Expected strengths compared with single lite sources:

- Better country stability through consensus and RIR registry separation.
- Better city stability where MaxMind, IP2Location, DB-IP, Sypex, and geofeeds
  agree.
- Better CIS handling when Sypex is available.
- Better postal/city fill rate when GeoNames postal data can safely enrich an
  otherwise incomplete record.
- Cleaner display fields through output normalization and mojibake repair.
- More transparent precision through `accuracy_radius_km` instead of implying
  exact point accuracy.

Expected limitations:

- It is not a substitute for a licensed premium database when contractual SLA,
  redistribution rights, mobile-carrier accuracy, or frequently updated
  enterprise network attribution is required.
- The seed range layout comes from DB-IP Lite. If a prefix is missing there, the
  builder will not invent it from other sources.
- City-level accuracy remains probabilistic. IP geolocation should be treated as
  region/city guidance, not a physical address signal.
- Geofeeds improve operator-owned ranges but can be narrow in coverage.

A reasonable expectation is:

- Country-level quality should usually match or exceed lite databases.
- City-level quality should often exceed a single lite source when at least two
  independent inputs agree, but it will not consistently match premium feeds for
  mobile, VPN, enterprise, or fast-moving allocations.
- Postal code quality should be treated as opportunistic enrichment unless the
  source consensus and `accuracy_radius_km` support it.

## Quality Gate

After every build, `geo.sh` snapshots the previous `release/geo.csv` as
`release/geo.previous.csv`, runs `cmd/qualitycheck`, and writes
`release/geo-quality-report.txt`.

The report includes:

- Record count.
- Country, city, postal, and GeoNames-id coverage.
- Average confidence and low-confidence share.
- Added and removed prefixes.
- Country/city/postal/geoname regressions.
- Country and city changes.
- Text artifact count after cleanup.
- MMDB smoke lookups against sample IPs.

By default the quality check fails only if the MMDB is unreadable or sample
lookups return invalid records. Strict mode can be enabled with:

```bash
QUALITY_STRICT=1 ./geo.sh
```

Strict mode also fails on significant coverage loss, suspicious text artifacts,
or large confidence regressions.

## Normalization

Final records are normalized immediately before writing:

- Coordinates are rounded to five decimal places.
- City parentheticals are removed when they are clearly neighborhoods or labels.
- Common admin prefixes and suffixes are normalized for Cyrillic, Korean,
  Kazakh, Uzbek, Arabic, Vietnamese, Japanese, Spanish, and Portuguese forms.
- Common mojibake sequences are repaired.
- Duplicate state/city pairs are collapsed.
- Time zone is computed from final coordinates.

Normalization is intentionally conservative. It does not rewrite official
prefecture names such as `Tokyo-to`, `Osaka-fu`, or `Hokkaido`, and it does not
perform broad cross-language city renaming at the output layer.

## Geofeeds

Allowlisted RFC 8805 feeds are configured in:

```text
data/geofeeds/allowlist.tsv
```

The downloader installs each feed as `data/geofeeds/<name>.csv`. The parser
accepts both four-column and five-column feed rows:

```text
prefix,country,region,city
prefix,country,region,city,postal
```

`GEOFEED_MAX_IPV4_BITS=24` is the default floor for IPv4 geofeed entries. This
keeps host-level or extremely narrow entries from inflating the output tree.

## Publication

The code can be published separately from the downloaded databases. Do not
commit:

- `admin.env`
- downloaded files under `data/`
- generated files under `release/`
- API tokens, account IDs, license keys, or authenticated download URLs

Review [THIRD_PARTY_DATA.md](THIRD_PARTY_DATA.md) before distributing any
derived database.
