# KBART report for Keepers Registry

The Internet Archive acts as a repository for scholarly materials preserved as
part of [the Keepers Registry](https://keepers.issn.org/). Every year we submit
a report to them of our relevant holdings.

## Process


1. Make a new directory in here for the current year
2. Obtain the latest ISSN to ISSNL mapping file from issn.org. This link worked as of August 2025:
  - https://www.issn.org/wp-content/uploads/2014/03/issnltables.zip
3. Obtain the listing of serials held in petabox. This requires, as of August 2025, talking to Charles Horn. If he's not around, ask in `#scholarly-web` in slack.
  - link should look like https://archive.org/download/internetarchive-kbart/InternetArchive_Serials_KBART_20250731.zip
4. Run `convert_sim_kbart.py <serials file from charles> <issn to issnl mapping file>` and direct that output to a new file called `ia_sim_keepers_kbart.tsv`
5. Export the needed variables for `fatcat-cli` to function
  - `export FATCAT_SEARCH_HOST=http://wbgrp-svc500.us.archive.org:9200`
  - `export FATCAT_API_HOST=https://scholar.archive.org/_fc`
7. Run `search_fatcat_containers.sh > search_containers.json`
8. Convert `search_containers.json` to kbart format
  1. `cat search_containers.json | ./fatcat_kbart.py --json 2> error_containers.log > kbart_containers.json`
  2. `cat kbart_containers.json | ./fatcat_kbart.py --from-existing > fatcat_kbart.tsv`
9. Combine the two files
  1. `cp fatcat_kbart.tsv ia_serials_combined_kbart.tsv`
  2. `tail -n+2 ia_sim_keepers_kbart.tsv >> ia_serials_combined_kbart.tsv`
10. Rename the files to include dates, then upload them to IA:
  1. `ia upload ia-keepers-registry-kbart fatcat_kbart.2025-08-06.tsv ia_sim_keepers_kbart.2025-08-06.tsv ia_serials_combined_kbart.2025-08-06.tsv`
11. Upload the files to keepers directly via ftp:
  1. `ftp-ssl ftp.issn.org`
  2. `ftp> put ia_serials_combined_kbart.2025-08-06.tsv`

## ISSN.org FTP Upload

Username is `InternetArchive`; password is stored in the `ait-ansible` repo in `prod/group_vars/scholar/vault`. 

```bash
# start in local working directory with the KBART file to upload
ftp ftp.issn.org
ftp> put ia_serials_combined_kbart.2025-08-06.tsv
```

## Future

This ought to be automated but for something done once a year it has not felt super urgent. One important thing to note is that `search_containers.sh` uses `fatcat-cli` which is deprecated.
