#/usr/bin/env bash

set -e              # fail on error
set -u              # fail if variable not set in substitution
set -o pipefail     # fail if part of a '|' command fails

fatcat-cli search containers --index-json --limit 0 \
    '((preservation_bright:>=20 AND preservation_none:<=5 AND preservation_dark:<=5 AND preservation_shadows_only:<=5) OR (preservation_bright:>=200 AND preservation_none:<=20 AND preservation_dark:<=20 AND preservation_shadows_only:<=20) OR (preservation_bright:>=1000 AND preservation_none:<=100 AND preservation_dark:<=100 AND preservation_shadows_only:<=100))' \
    '!container_type:archive' '!container_type:repository' '!publisher_type:big5' \
    '(issne:* OR issnp:*)' 'issnl:*' \
