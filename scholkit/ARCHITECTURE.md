# ARCHITECTURE

scholkit is an scratch space to build tools to support large scale
bibliographic catalog creation, preservation activities and data derivations.

For now, mostly data normalization and grouping. Normalize many different data
sources into a few formats: release, work (clustered releases), container, file
and contrib.

* release: a single record describing a bibliographic entity
* work: a group of releases closely related (different versions, pre-print, published, ...)
* container: collection of releases
* file: a web location for a release
* contrib: a contributor to a release

The goal would be to shrink each operation down to a single command then use
outside orchestration, scripting or deployment options to automate things.
