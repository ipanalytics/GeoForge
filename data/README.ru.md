_English version: [README.md](README.md)_

# data/

Разместите здесь исходные базы данных:

- `dbip-city-lite.csv` — DB-IP City Lite, база-затравка (seed) (определяет, какие блоки CIDR существуют). Обязательна.
- `GeoLite2-City.mmdb` — MaxMind GeoLite2 City. Необязательна, но рекомендуется.
- `IP2LOCATION-LITE-DB5.BIN` — IP2Location Lite DB5. Необязательна, но рекомендуется.
- `SxGeoCity.dat` — Sypex Geo. Необязательна; используется только для стран СНГ.

Архив почтовых индексов GeoNames (`allCountries.zip`) автоматически загружается в этот каталог при первом запуске пакетом `geozip`.

`../scripts/download_data.sh` может загружать и распаковывать базы-доноры под именами файлов, которые ожидает сборщик. Токены читаются из переменных окружения и не хранятся в репозитории:

```bash
export IP2LOCATION_TOKEN='...'
export MAXMIND_ACCOUNT_ID='...'
export MAXMIND_LICENSE_KEY='...'
scripts/download_data.sh
```

Полезные переопределения:

- `DBIP_MONTH=2026-05`
- `DBIP_CITY_CSV_URL=https://download.db-ip.com/free/dbip-city-lite-2026-05.csv.gz`
- `IP2LOCATION_URL='https://www.ip2location.com/download?...'`
- `MAXMIND_URL='https://download.maxmind.com/geoip/databases/GeoLite2-City/download?suffix=tar.gz'`
- `FORCE_DOWNLOAD=1`
- `DOWNLOAD_ONLY=ip2location` (или `dbip`, `sypex-city`, `sypex-country`, `maxmind`)
- `DOWNLOAD_ONLY=rir` для обновления массовой статистики делегирований RIR.
- `DOWNLOAD_ONLY=geofeeds` для обновления входящих в белый список geofeeds RFC 8805 из `data/geofeeds/allowlist.tsv`.
- `STATE_FILE=data/download-state.tsv`
- `GEOFEED_MAX_IPV4_BITS=24` — задаёт нижнюю границу длины префикса geofeed для сборщика. По умолчанию записи IPv4, более специфичные, чем `/24`, игнорируются, чтобы фиды, перегруженные `/32`, не раздували MMDB и не подменяли широкий консенсус записями уровня отдельного хоста.
- `AUTO_DOWNLOAD=0` для `geo.sh`, если нужно пропустить загрузки.
- `FORCE_BUILD=1` для `geo.sh`, если нужно выполнить пересборку, даже когда все источники не изменились.

Загрузчик всегда сначала распаковывает данные во временный файл и сравнивает SHA256 с установленной в данный момент базой. Если содержимое не изменилось, прежняя база сохраняется, а временный файл удаляется. Заголовки `ETag` / `Last-Modified` сохраняются в `download-state.tsv`, если источник их отдаёт, но окончательное решение принимается по сравнению контрольных сумм.

`geo.sh` читает `data/download-changed.txt`; если в нём `0` и `release/geo.mmdb` уже существует, сборка пропускается и существующий MMDB сохраняется.

Массовые бесплатные (non-premium) источники:

- `data/rir/delegated-*-extended-latest` загружается с пяти RIR и используется только для `registry_country_code`. Это позволяет избежать лимитов запросов RDAP/WHOIS.
- `data/geofeeds/allowlist.tsv` управляет приёмом geofeeds RFC 8805. Загружаются только явно внесённые в белый список URL; каждый фид рассматривается как авторитетный кандидат для своих префиксов, но всё равно проходит через консенсус. Хорошие недорогие источники — это RFC 8805-фиды, публикуемые операторами, публичный opt-in-фид OpenGeoFeed, ежедневное валидированное объединение GeolocateMuch, а также ссылки `geofeed:` в WHOIS/RPSL записях RIR, обнаруженные в массовых выгрузках реестров. Фиды вендоров, такие как публичный GeoIP CSV от Fortinet, полезны для собственных сетей соответствующего вендора, но их не следует считать универсальной базой данных городов.

Если присутствует только `dbip-city-lite.csv`, сборка всё равно завершится успешно — она просто выполнится без каких-либо проверок консенсуса (один источник = одна запись).
