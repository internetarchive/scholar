package openalex

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

func configureSourcesViper() {
	viper.Set("openalex_sources.external_task_queue", "ext")
	viper.Set("openalex_sources.internal_task_queue", "int")
	viper.Set("openalex_sources.max_consecutive_errors", 7)
}

func Test_OpenAlexSourcesWorkflow_FetchesThenImports(t *testing.T) {
	configureSourcesViper()
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(FetchSourcesActivity, mock.Anything, FetchSourcesInput{Date: "2026-10-14"}).
		Return(FetchSourcesOutput{S3Key: "scholkit/openalex/sources/2026/x.jsonl.gz", Records: 10}, nil).Once()

	var got ImportSourcesInput
	env.OnActivity(ImportSourcesActivity, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) { got = args.Get(1).(ImportSourcesInput) }).
		Return(containers.Counts{Rows: 10, Added: 1, Ignored: 9}, nil).Once()

	env.ExecuteWorkflow(OpenAlexSourcesWorkflow, SourcesWorkflowInput{Date: "2026-10-14"})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var counts containers.Counts
	require.NoError(t, env.GetWorkflowResult(&counts))
	require.Equal(t, containers.Counts{Rows: 10, Added: 1, Ignored: 9}, counts)

	require.Equal(t, "scholkit/openalex/sources/2026/x.jsonl.gz", got.S3Key)
	require.Equal(t, 7, got.MaxConsecutiveErrors)
	require.True(t, strings.HasPrefix(got.Source, "openalex-sources-20261014-"), got.Source)
	env.AssertExpectations(t)
}

// Given a day already in S3, the workflow imports it without fetching.
func Test_OpenAlexSourcesWorkflow_S3KeySkipsFetch(t *testing.T) {
	configureSourcesViper()
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(ImportSourcesActivity, mock.Anything, ImportSourcesInput{
		S3Key:                "scholkit/old.jsonl.gz",
		Source:               "backfill",
		MaxConsecutiveErrors: 7,
	}).Return(containers.Counts{}, nil).Once()

	env.ExecuteWorkflow(OpenAlexSourcesWorkflow, SourcesWorkflowInput{
		S3Key:          "scholkit/old.jsonl.gz",
		SourceOverride: "backfill",
	})

	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

func Test_OpenAlexSourcesWorkflow_FetchFailureFails(t *testing.T) {
	configureSourcesViper()
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.OnActivity(FetchSourcesActivity, mock.Anything, mock.Anything).
		Return(FetchSourcesOutput{}, errors.New("openalex is down"))

	env.ExecuteWorkflow(OpenAlexSourcesWorkflow, SourcesWorkflowInput{Date: "2026-10-14"})

	require.ErrorContains(t, env.GetWorkflowError(), "openalex is down")
}

func Test_OpenAlexSourcesWorkflow_RequiresDateOrKey(t *testing.T) {
	configureSourcesViper()
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	env.ExecuteWorkflow(OpenAlexSourcesWorkflow, SourcesWorkflowInput{})

	require.ErrorContains(t, env.GetWorkflowError(), "one of Date or S3Key is required")
}
