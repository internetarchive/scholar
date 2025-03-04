# table by table, issue the COPY via psql
# for some tables, indices will have to be removed and then re-added
# special handling for release foreign key restoration


source ./dump.sh

DB="fatcat2"
PSQL="psql -d $DB"

echo "restoring containers..."
cat << EOF | $PSQL
  COPY fcapi_container (legacy_ident, name, extra, container_type, publisher, issnl, issne, issnp, wikidata_qid, source)
  FROM '$CONTAINERS_OUT'
  WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '', FORCE_NULL (extra)); 
EOF

echo "restoring creators..."
cat << EOF | $PSQL
  COPY fcapi_creator (legacy_ident, display_name, given_name, surname, orcid, source, extra)
  FROM '$CREATORS_OUT'
  WITH (FORMAT CSV, DELIMITER E'\t', HEADER, NULL '', FORCE_NULL (extra));
EOF

echo "restoring works..."
echo "- dropping indices"

# TODO drop indices

echo "- reading from tsv"

# TODO COPY

echo "- restoring indices"

# TODO restore indices


