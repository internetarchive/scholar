package doaj

import (
	"errors"
	"strings"
	"testing"

	"git.archive.org/webgroup/scholar/trawler/containers"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

func configureJournalsViper() {
	viper.Set("doaj_journals.external_task_queue", "ext")
	viper.Set("doaj_journals.internal_task_queue", "int")
	viper.Set("doaj_journals.max_consecutive_errors", 7)
}

func Test_DOAJJournalsWorkflow_FetchesThenImports(t *testing.T) {
	configureJournalsViper()
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(FetchJournalCSVActivity, mock.Anything).
		Return(FetchJournalCSVOutput{S3Key: "scholkit/doaj/journals/2026/x.csv"}, nil).Once()

	var got ImportJournalsInput
	env.OnActivity(ImportJournalsActivity, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) { got = args.Get(1).(ImportJournalsInput) }).
		Return(containers.Counts{Rows: 10, Added: 1, Ignored: 9}, nil).Once()

	env.ExecuteWorkflow(DOAJJournalsWorkflow, JournalsWorkflowInput{})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var counts containers.Counts
	require.NoError(t, env.GetWorkflowResult(&counts))
	require.Equal(t, containers.Counts{Rows: 10, Added: 1, Ignored: 9}, counts)

	require.Equal(t, "scholkit/doaj/journals/2026/x.csv", got.S3Key)
	require.Equal(t, 7, got.MaxConsecutiveErrors)
	require.True(t, strings.HasPrefix(got.Source, "doaj-journals-"), got.Source)
	env.AssertExpectations(t)
}

// Given a CSV already in S3, the workflow imports it without fetching.
func Test_DOAJJournalsWorkflow_S3KeySkipsFetch(t *testing.T) {
	configureJournalsViper()
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(ImportJournalsActivity, mock.Anything, ImportJournalsInput{
		S3Key:                "scholkit/old.csv",
		Source:               "backfill",
		MaxConsecutiveErrors: 7,
	}).Return(containers.Counts{}, nil).Once()

	env.ExecuteWorkflow(DOAJJournalsWorkflow, JournalsWorkflowInput{
		S3Key:          "scholkit/old.csv",
		SourceOverride: "backfill",
	})

	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

func Test_DOAJJournalsWorkflow_FetchFailureFails(t *testing.T) {
	configureJournalsViper()
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(FetchJournalCSVActivity, mock.Anything).
		Return(FetchJournalCSVOutput{}, errors.New("doaj is down"))

	env.ExecuteWorkflow(DOAJJournalsWorkflow, JournalsWorkflowInput{})

	require.ErrorContains(t, env.GetWorkflowError(), "doaj is down")
}
