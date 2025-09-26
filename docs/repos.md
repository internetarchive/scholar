# Scholar-related Gitlab repositories

If you are reading this document you are looking at the scholar monorepo. Like many monorepos, it is aspirational. As of September 2025, scholar's very soul remains fragmented across the following list of projects.

I have included my (nate's) personal thoughts about each one.

- bnewbold/scratch
  - copious, largely unstructured notes from the original scholar engineer
- martin/scratch
  - martin's scratch space
- nsmith/scratch
  - nate's scratch space
- webgroup/arabesque
  - I have yet to use this but it's related to crawl log analysis
- webgroup/chocula
  - I remain not fully clear on this project but it has to do with finding out about journals that exist. It should be consumed by the monorepo.
- webgroup/dcdump
  - tool by martin for dealing with datacite data
- webgroup/fatcat
  - at this point just the workers for scraping upstream/importing their stuff.
  - used to have fatcat.wiki frontend (code is still in there)
- webgroup/fatcat-cli
  - technically still usable but it's got some weird behavior around auth that make it frustrating. direct API access is easier.
- webgroup/fatcat-scholar
  - scholar.archive.org and scholar.archive.org/fatcat
  - fastapi though it shouldn't be; should just be an "app" within djscholar
- webgroup/fuzzycat
  - some code for fuzzy citation matching; should be inlined
  - there is a copy of it on pypi but i don't have creds for managing it
- webgroup/ia-lockss-infra
  - i literally just found this
- webgroup/journal-crawls
  - configs and notes for one-off (or "periodic" crawls) 
- webgroup/journal-infra
  - old ansible repo for scholar
  - still some roles in here we use
  - deprecated in favor of ait-ansible
- webgroup/pdf_trio
  - unused afaik
- webgroup/refcat
  - stuff for martin's citation graph research
- webgroup/sandcrawler
  - the heart of the daily crawl stuff -- spn wrapper, custom crawling logic, coordination of PDF processing
- webgroup/scholar
  - you're looking at it
  - eventually should be open sourced to github but isn't yet
- webgroup/selfless
  - i have not looked in here
