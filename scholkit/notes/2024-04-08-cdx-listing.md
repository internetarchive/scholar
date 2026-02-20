# cdx listing notes

* list of items

```shell
$ time grep '[-A-Z]*-20[0-9][0-9]-[0-9][0-9]"' ../notes/items.ndj | \
    jq -rc .identifier | \
    parallel -j 4 -I {} ./cdx_collection.py {}
```

