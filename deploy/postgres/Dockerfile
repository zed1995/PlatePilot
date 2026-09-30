# PostgreSQL with pgvector and PostGIS in one image.
#
# Neither upstream image carries both extensions on arm64:
#   - pgvector/pgvector has PostGIS only in the Debian apt repository
#   - postgis/postgis publishes amd64 only
# So the Debian base of pgvector is used and PostGIS is added on top. Building
# from the pgvector image keeps the vector extension version aligned with the
# tag, which is what the partial-HNSW index strategy depends on.
FROM pgvector/pgvector:0.8.6-pg17-trixie

# -sizes are needed because PostGIS pulls in GEOS/PROJ via pg_config.
RUN set -eux; \
	apt-get update; \
	apt-get install -y --no-install-recommends \
		postgresql-17-postgis-3 \
		postgresql-17-postgis-3-scripts; \
	rm -rf /var/lib/apt/lists/*

# Schema changes are NOT applied here. Migrations live in
# shared/adapter/repository/postgres/migrations and are embedded into the
# binary, applied by `data-pipeline migrate`. Copying them into
# /docker-entrypoint-initdb.d would run them only on first initialisation of an
# empty data directory, so an existing volume would silently keep an old schema
# and the SQL would exist in two places that could drift.
