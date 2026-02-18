# IA Scholar Project

This monorepo\* contains code and documentation for the scholar project.

The public's main entry point into scholar is [https://scholar.archive.org], a
full text search engine over PDFs whose text we have indexed. We also offer
[fatcat](https://scholar.archive.org/fatcat), a bibliographic database.

The code for those entry points is not here, however: this monorepo is where
development is occurring on the next version of Scholar's offerings. 

The projects here:

- `djscholar`, a django project that houses the new fatcat2 api server and will
  eventually house new frontends for scholar and fatcat's homepages.
- `trawler`, scholar's new mechanism for daily discovery of open access scholarly content
- `scholkit`, a CLI for scraping scholarly publishing metadata
- `blobproc`, an uncomplicated service for processing PDF files
- `fcmigrate`, a large pile of python for moving data from fatcat1's postgresql -> fatcat2 postgresql
- `scholstats`, a scrappy python script for generating emails about scholar project statistics
- `pubmed2json`, a cli/library for converting PubMed XML to JSON
- `kbart`, documentation and code for generating the annual keepers' registry report
- `scripts`, infrequently used or one off programs in various languages

All infrastructure related code is tracked internally and is not here.

\* a lot of the scholar project still exists in other [repos](docs/repos.md),
some of which are internal to IA and some which are on GitHub.

## Open Source Policy

For now, the Scholar project is more of a _source available_ project. We want
others to be able to learn from and re-use our code where possible but due to
time constraints it's not feasible to offer any kind of guarantee on when or if
we'll notice issues or pull requests.

## Authors

- [Nate Smith](https://github.com/vilmibm)
- [Martin Czygan](https://github.com/miku)
- [Michael Della Bitta](https://github.com/mdellabitta)
- Much of this is based directly on work by [Bryan Newbold](https://github.com/bnewbold)

## License

Internet Archive Scholar
Copyright (C) 2026 Internet Archive

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published
by the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
