package counts

type ReleaseCounts struct {
	// Skipped is the count of lines in the upstream we knew we would never want
	Skipped int
	// Ignored is the count of lines in the upstream metadata we were already aware of
	Ignored int
	// Added is the count of lines from the upstream metadata we added to Fatcat
	Added int
	// CrawlWanted is the count of how many releases we tried to get from the web
	CrawlWanted int
	// Acquired is the count of PDFs we acquired from the upstream metadata
	Acquired int
	// Ingested is the count of PDFs we successfully ingested into scholar's search index
	Ingested int
}

type ContainerCounts struct {
	// Ignored is the count of containers fatcat already knew about
	Ignored int
	// Added is the count of containers we created in fatcat
	Added int
	// Skipped is the count of containers we did not create because of data issues
	Skipped int
}

type PdfCounts struct {
	// Skipped is the count of PDFs already known (checksum with content) to fatcat
	Skipped int
	// Processed is the count of PDFs successfully handled via blobproc
	Processed int
	// Failed is the count of PDFs that failed to be parsed
	Failed int
}

type Counts struct {
	Releases   ReleaseCounts
	Containers ContainerCounts
	Pdfs       PdfCounts
}

func (c Counts) Add(other Counts) Counts {
	return Counts{
		Releases: ReleaseCounts{
			Skipped:     c.Releases.Skipped + other.Releases.Skipped,
			Ignored:     c.Releases.Ignored + other.Releases.Ignored,
			Added:       c.Releases.Added + other.Releases.Added,
			CrawlWanted: c.Releases.CrawlWanted + other.Releases.CrawlWanted,
			Acquired:    c.Releases.Acquired + other.Releases.Acquired,
			Ingested:    c.Releases.Ingested + other.Releases.Ingested,
		},
		Containers: ContainerCounts{
			Ignored: c.Containers.Ignored + other.Containers.Ignored,
			Added:   c.Containers.Added + other.Containers.Added,
		},
		Pdfs: PdfCounts{
			Skipped:   c.Pdfs.Skipped + other.Pdfs.Skipped,
			Processed: c.Pdfs.Processed + other.Pdfs.Processed,
			Failed:    c.Pdfs.Failed + other.Pdfs.Failed,
		},
	}
}
