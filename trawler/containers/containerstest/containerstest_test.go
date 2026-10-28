package containerstest

import (
	"testing"

	"git.archive.org/webgroup/scholar/trawler/issn"
	"github.com/stretchr/testify/require"
)

func Test_NewISSN_NotInTable(t *testing.T) {
	require.Equal(t, NewISSN, issn.Normalize(NewISSN))
	require.Equal(t, "", issn.ISSN2ISSNL(NewISSN),
		"the ISSN-L table now knows NewISSN; pick another unassigned one")
}
