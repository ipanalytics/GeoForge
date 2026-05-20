# release/

Build output lands here.

- `geo.mmdb` — final compiled MMDB, the deliverable.
- `geo.csv` — flat audit copy of the same records, used by the quality checker.
- `geo.previous.csv` — previous audit copy saved before a new build.
- `geo-quality-report.txt` — post-build quality report comparing the new
  release against the previous audit copy.

Keep this folder in sync with whatever path your PHP / app expects:
the example `index.php` reads from `db/geo.mmdb`, so on deploy you would
typically copy `release/geo.mmdb` to `<docroot>/db/geo.mmdb`.
