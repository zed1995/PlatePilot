# Boundary data

Reference geometry that is fact data rather than code, so the raw download is
git-ignored (`data/boundaries/*.geojson`) and only its provenance is committed.

## nyc-borough-boundaries-water.geojson

| Field | Value |
|---|---|
| Purpose | Assign `borough_guess` to each restaurant during the meta stage |
| Publisher | NYC Department of City Planning, "BYTES of the BIG APPLE" |
| Dataset | Borough Boundaries (water areas included) |
| Dataset id | `wh2p-dxnf` |
| Version | 26b |
| Source URL | https://data.cityofnewyork.us/resource/wh2p-dxnf.json?$limit=100 |
| Landing page | https://data.cityofnewyork.us/City-Government/Borough-Boundaries-water-areas-included-/wh2p-dxnf |
| Licence | NYC Open Data terms of use |
| SHA-256 | `9c271db18ea8a76b2c9f030159710e45cfd33e236285ad84b8c18c408cdb1591` |
| Size | 142622 bytes |
| Contents | 5 boroughs, 7 rings, 3632 vertices |

The water-inclusive variant was chosen over `yqww-f9f3` (water excluded) because
Queens' western edge reaches longitude -74.0422 in this version: the harbour and
river areas belong to the borough, which is both what a user means by "Queens"
and what prevents river-crossing restaurants from being misattributed.

Refetch with:

```bash
curl -sS -L -o data/boundaries/nyc-borough-boundaries-water.geojson \
  "https://data.cityofnewyork.us/resource/wh2p-dxnf.json?\$limit=100"
shasum -a 256 data/boundaries/nyc-borough-boundaries-water.geojson
```

The checksum is verified by `curate.LoadBoundaries`, so a truncated or edited
download fails the import instead of silently changing every borough label.
