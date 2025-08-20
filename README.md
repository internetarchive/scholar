## IA Scholar Project

This monorepo\* contains code and documentation for the scholar project.

The public's main entry point into scholar is [https://scholar.archive.org], a full text search engine over PDFs whose text we have indexed. We also offer [fatcat](https://scholar.archive.org/fatcat), a bibliographic database.

the most significant thing in here so far is `djscholar`, a django project that currently houses the new fatcat2 api server but will eventually house the frontends for scholar and fatcat's homepages.

`fcmigrate` is a big pile of python for moving data from the old fatcat database to the new fatcat2 database.

`scholstats` generates the daily/weekly email about scholar statistics.

the `trawler` project is a nascent attempt at porting the current kafka/systemd/python daily crawl work into Go running in Temporal.

`kbart` has documentation and code for generating the annual keepers' registry report.

`bash` is for one-off scripts and currently just has one for checking DOIs against the fatcat2 API.
