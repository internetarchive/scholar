# KBART report for Keepers Registry

The Internet Archive acts as a repository for scholarly materials preserved as
part of [the Keepers Registry](https://keepers.issn.org/). Twice a year we submit
a report to them of our relevant holdings.

The report combines two sources of holdings, both emitted as
[KBART](https://en.wikipedia.org/wiki/KBART)-format TSV:

- **fatcat containers** — journals we preserve, selected from the
  `fatcat_container` Elasticsearch index and checked for eligibility against the
  fatcat v2 API.
- **IA SIM serials** — scanned periodicals, converted from an IA-provided KBART
  file with ISSN-L added via an ISSN-to-ISSN-L lookup.

This directory holds the `kbart` Go program that generates the report, plus a
dated directory per submission with its inputs and outputs.

## Frequency

Uploads should occur in June and January.

## The `kbart` tool

Build it (Go 1.26+):

```bash
go build -o kbart .
```

It has one command per pipeline stage plus an `all` command that runs the whole
thing:

| command   | purpose                                                                    |
| -------   | -------                                                                    |
| `search`  | query `fatcat_container` for candidate preserved containers (emits idents) |
| `report`  | check candidate idents for eligibility, emit fatcat KBART rows             |
| `sim`     | convert an IA SIM serials KBART file, adding `linking_issn`                |
| `combine` | concatenate KBART files into one, keeping a single header                  |
| `all`     | run search → report → sim → combine end-to-end                             |

Endpoints default to the public fatcat v2 API
(`https://scholar.archive.org/api/fatcat/v2`) and the scholar Elasticsearch
endpoint (`https://scholar.archive.org/_es`). **The `_es` endpoint is only
reachable on the IA VPN**, so `search` and `all` must be run from the VPN. The
fatcat v2 API used by `report` is public. Override with `--es-host` / `--api-host`
if needed. See `scholar-and-fatcat.md` for API details.

## Process

1. Make a new directory here for the current submission, named for the year and
   month (e.g. `202607`).
2. Obtain the latest ISSN-to-ISSN-L mapping from issn.org and unzip it. This link
   worked as of 2026:
   - https://www.issn.org/wp-content/uploads/2014/03/issnltables.zip
   - The file you want from the zip is `<date>.ISSN-to-ISSN-L.txt`.
3. Obtain the listing of serials held in petabox. Look in the item https://archive.org/download/internetarchive-kbart for a file named `InternetArchive_Serials_KBART_YYYYMMDD.zip`. If there isn't a recent enough file, ask Charles Horn in slack or post for help in `#scholarly-web`.
4. From the IA VPN, run the whole pipeline. This writes three dated files into
   `--outdir`:

   ```bash
   go build -o kbart .
   ./kbart all \
       --sim-file   202607/InternetArchive_Global_Serials_20260707.txt \
       --issn-map   202607/20260707.ISSN-to-ISSN-L.txt \
       --outdir     202607
   ```

   Output files (`<date>` defaults to today):
   - `fatcat_kbart.<date>.tsv` — eligible fatcat containers
   - `ia_sim_keepers_kbart.<date>.tsv` — converted SIM serials
   - `ia_serials_combined_kbart.<date>.tsv` — the combined report to submit

   The full container search + eligibility check makes tens of thousands of
   fatcat API calls and takes a while; tune with `--concurrency`. To run stages
   separately (useful for debugging), see the per-stage commands below.
5. Upload the files to IA:

   ```bash
   ia upload ia-keepers-registry-kbart \
       202607/fatcat_kbart.2026-07-07.tsv \
       202607/ia_sim_keepers_kbart.2026-07-07.tsv \
       202607/ia_serials_combined_kbart.2026-07-07.tsv
   ```
6. Upload the combined file to Keepers directly via FTP (see below).
7. Upload the raw workspace to petabox in case it needs to be referred to:
   ```bash
   zip -r 202607.zip 202607
   ia upload scholar-raw-kbart-workfiles 202607.zip -m 'collection:ia_biblio_metadata'
   ```

### Running stages separately

`all` is equivalent to:

```bash
# 1. candidate container idents
./kbart search > 202607/containers.txt

# 2. eligible fatcat KBART rows (--dump-json / --from-json cache the API fetches)
./kbart report -i 202607/containers.txt -o 202607/fatcat_kbart.2026-07-07.tsv

# 3. convert the SIM serials file
./kbart sim \
    202607/InternetArchive_Global_Serials_20260707.txt \
    202607/20260707.ISSN-to-ISSN-L.txt \
    -o 202607/ia_sim_keepers_kbart.2026-07-07.tsv

# 4. combine (fatcat first, so its header is kept)
./kbart combine \
    202607/fatcat_kbart.2026-07-07.tsv \
    202607/ia_sim_keepers_kbart.2026-07-07.tsv \
    -o 202607/ia_serials_combined_kbart.2026-07-07.tsv
```

`report` prints a tally of eligibility outcomes to stderr (how many containers
were rejected for each reason); pass `-v` to log every container's status.

## ISSN.org FTP Upload

Username is `InternetArchive`; password is stored in the `ait-ansible` repo in
`prod/group_vars/scholar/vault`.

```bash
# start in the local working directory with the KBART file to upload
ftp> put ia_serials_combined_kbart.2026-07-07.tsv
```

On recent macOS you can use `ncftp -u InternetArchive -p <password> ftp://ftp.issn.org` after installing `ncftp` from `brew`.
