# daily data feeds

Fetch and save data from:

* crossref (d)
* datacite (d)
* pubmed (d)
* 150K+ OAI PHM endpoints (d), this includes arxix, doaj
* openalex (m)

Wrap logic into a single tool, everything saved in files on disk, compressed.
Could be triggered with systemd timer, to run once a day.

```
$ skolfeed
```

PS. New attempt to group various executables w/ a common prefix, e.g. scholkit,
skit, skol, ...
