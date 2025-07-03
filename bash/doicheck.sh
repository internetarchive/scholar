# this is a one-off script for checking if we have some DOIs from a small (2500 row) tsv.
if [ -z "$1" ]; then
  echo "need tsv file path"
  exit 1
fi

IN=$1

lookup() {
  curl -sG \
  "https://scholar.archive.org/api/fatcat/v2/release/lookup/fulltext" \
  -d "id_type=doi" -d "id_value=${1}" -w '%{redirect_url}'
}

#header=$(head -n1 $IN | tr -d '\r')
#printf '%s\tpdfLink\n' "$header"
#
#tail -n+2 $IN | while read x; do
#  doi=$(echo "$x" | cut -f2)
#  ft_url=$(lookup "$doi")
#  printf '%s\t%s\n' "$x" "$ft_url"
#done

tail -n+2 $IN | while read x; do
  doi=$(echo "$x" | cut -f2)
  ft_url=$(lookup "$doi")
  if [ -n "$ft_url" ]; then
    if echo $ft_url | grep -qv "Not Found"; then
      printf '%s\t%s\n' "$doi" "$ft_url"
    fi
  fi
done
