# Scholar Daily Crawl

## Upstreams

Every day we consume metadata from various sources. We use the metadata to populate records in Fatcat and, should the metadata point to a location on the web, use it to find PDFs for indexing.

Our sources:

- **Crossref**. An organization that mints DOIs for academic articles. They publish a free feed of any articles they have touched in their database (created/updated). This is our biggest source of papers. Some days produce millions of rows worth of updates.
- **Doaj**. A directory of open access journals. Smaller payloads but a guarantee of material we can freely index and redistribute.
- **Pubmed**. A national organization for medical research. Has its own ID system but material in pubmed also tends to carry DOIs. There exists both a European and American pubmed; their holdings are largely the same.
- **ArXiv**. An archive of pre-prints. Lots of stuff. Easy to access.
- **Datacite**. An organization that mints DOIs for academic materials: both articles and datasets. We do track the datasets in Fatcat.

Whenever we encounter a DOI we use `https://doi.org/<DOI>` to get a redirect to a landing page where we hope to find a PDF to download. Sometimes it's easy and the landing page has a `<meta name="citation_pdf_url">` element we can just consume. Other times it's less obvious.

## Temporal

The daily crawls are orchestrated using Temporal and can be found in the `scholar_trawler` namespace. A schedule runs one crawl workflow for each upstream. These workflows can take days and sometimes weeks to complete; we allow them to pile up and overlap. Expect a steady state of about 100 workflows running concurrently.

The worker queues are split up into two distinct workers per upstream: external and internal. We use the external queue for downloading data from the upstreams; we use the internal queue for everything else.

Check ansible to see which hosts run the workers (`scholar_trawler` group).

## Health

You can learn about the health of the daily crawls by checking Temporal either via the terminal or its GUI. You can also watch the logs for temporal workers like `trawler-worker-crossref-internal`.

If you just want to bask in the satisfying glow of incoming papers you can refresh fatcat's [changelog](https://scholar.archive.org/fatcat/changelog).

It shows you what records are being created and whence.

You can also keep an eye on statistics about PDF ingestion on scholar's [stats](https://scholar.archive.org/stats) page.

## Key systems

The daily crawl is somewhat complicated and relies on several systems to function beyond just Temporal.

- **trawler** is the Go program that does all the actual business logic of API consumption and record creation.
- **blobproc** is an abstraction over PDF processing tools. It runs in memory though was initially conceived as a standalone service.
- **grobid** is a hosted service called by blobproc; it uses machine learning to extract information from PDFs.
- **go-fitz** is a wrapper around **mupdf**. It is also called by blobproc and acts as a backup to grobid.
- **SPN** or Save Page Now is an API hosted by the Wayback Team we use to actually crawl the web and download PDFs.
- we call the **CDX API** offered by the wayback team to find out whether or not pages/pdfs already exist in wayback.
- **Elasticsearch** is where we index both metadata and full text. See the [elasticsearch docs](./elasticsearch.md) for more info.
- **Fatcat's V2 API** as exposed by `djscholar` is what we use to create, update, and read metadata records.
