package ezb

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// tsvHeader is the start of EZB's TSV export, as served.
const tsvHeader = "Total: 3 journal(s)\n\n" +
	"Title\tPublisher\tE-ISSN\tP-ISSN\tZDB-Number\tEZB-Id\tbibliographical URL(s)\tFull text URL\tFrontdoor URL\tJournal link\tType\tPrice type\tAccess requirements\tKeyword(s)\tSubject\tFirst volume\tFirst issue\tFirst year\tLast volume\tLast issue\tLast year\tMoving Wall\t\n"

// Real rows, trimmed. Journal of Business Models has its ZDB number in the
// P-ISSN column.
const tsvRows = "Analytical Communications (1997-1999)\tRoyal Society of Chemistry (RSC)\t1364-5536\t1359-7337\t1495496-5\t4\thttps://pubs.rsc.org/ac\t\thttps://ezb.ur.de?1495496&amp;bibid=UBR\thttps://pubs.rsc.org/ac\tFulltext, online and print\tfree of charge\tFree access, but probably without further rights of use\tAnalytische Chemie\tChemistry and Pharmacy\t34\t0\t1997\t36\t0\t1999\t\t\n" +
	"Perspectivas en Ciencias M&eacute;dicas\tPCMB\t3152-9666\t\t\t658441\thttps://perspectivascmb.com/a***https://perspectivascmb.com/b\t\t\t\tFulltext, online only\tfree of charge\tOpen Access\t\tMedicine\t1\t1\t2026\t0\t0\t\t\t\n" +
	"Journal of Business Models (2026-)\tEmerald\t2246-2465\t2807730-1\t2807730-1\t658442\thttps://www.emerald.com/jobm\t\t\t\tFulltext, online only\tsubject to fee\tSubscription with authentication mechanisms\t\tBusiness\t0\t0\t2026\t0\t0\t\t\t\n"

func Test_parseJournalsTSV(t *testing.T) {
	recs, err := parseJournalsAny(strings.NewReader("\xef\xbb\xbf" + tsvHeader + tsvRows))
	require.NoError(t, err)
	require.Len(t, recs, 3)

	ac := recs[0]
	require.Equal(t, 4, ac.EZBID)
	require.Equal(t, []string{"1359-7337"}, ac.PrintISSNs)
	require.Equal(t, []string{"1364-5536"}, ac.OnlineISSNs)
	require.Equal(t, flexYear(1997), ac.FirstYear)
	require.Equal(t, flexYear(1999), ac.LastYear)
	require.Equal(t, "1495496-5", ac.ZDBID)
	require.False(t, ac.isOA())

	pcmb := recs[1]
	require.True(t, pcmb.isOA())
	require.Equal(t, flexYear(0), pcmb.LastYear, "running")
	require.Equal(t, []string{"https://perspectivascmb.com/a", "https://perspectivascmb.com/b"}, pcmb.TitleURLs)
	require.Equal(t, map[string]any{"ids": []int{658441}, "price_type": "free", "first_year": 2026, "oa": true},
		ezbExtra(journalRow{Records: []journalRecord{pcmb}}))

	rows := groupJournals(recs)
	require.Len(t, rows, 3)
	for _, row := range rows {
		if row.Records[0].EZBID == 658442 {
			require.Equal(t, []string{"2246-2465"}, row.ISSNs, "the ZDB number in P-ISSN is no ISSN")
		}
		if row.Records[0].EZBID == 658441 {
			require.Equal(t, "Perspectivas en Ciencias Médicas", row.title())
		}
	}
}

func Test_parseJournalsTSV_CutShort(t *testing.T) {
	_, err := parseJournalsAny(strings.NewReader(tsvHeader + tsvRows + "A Journal Cut\tPubl"))
	require.ErrorContains(t, err, "cut short")

	_, err = parseJournalsAny(strings.NewReader("Total: 0 journal(s)\n\nnot a header\n"))
	require.ErrorContains(t, err, "no header row")
}
