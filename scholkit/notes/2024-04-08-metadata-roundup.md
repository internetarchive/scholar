# metadata roundup

Preservation of scholarly communications on the web relies on openly available
metadata. We use data from a variety of source to guide archiving and often we
ask: how comprehensive is our coverage and to what extent do data sources -
especially large aggregators - contain information about the same object?

## Combining large and small sources of metadata

In the past we included metadata gathered from aggregators and projects, such
as CrossRef, DataCite, DOAJ, PubMed, Arxiv and others into our continuous
cataloging pipeline. In addition, we used data from the (now defunkt) Microsoft
Academic Project, continued as OpenAlex for targeted web archiving. We also
expanded our metadata acquisition from the web by accessing over 150000 sites
implementing OAI-PMH, a protocol used by popular open source publishing tools
like Open Journal Systems or institutional repository software, such as DSpace,
among others. The most recent version of the this dataset, which we call
oaiscrape, included over 320M records (and is accessible at
[https://archive.org/details/oai_harvest_2023-11-01](https://archive.org/details/oai_harvest_2023-11-01)).

In early 2024 we ran an analysis across seven large bibliographic data sources
to understand the amount of overlap between these datasets. In this analysis,
we looked at overlaps based on DOI between CrossRef, DataCite, DOAJ,
DBLP, oaiscrape, OpenAlex and PubMed.

If we simply combine all these data sources we observe 430M DOI in total. There
are only about
[275M](https://www.doi.org/the-identifier/resources/factsheets/key-facts-on-digital-object-identifier-system)
DOI that exist, so how do these data sources overlap?

...
