package providers

import (
	"context"

	"github.com/Korbicorp/klovys99/internal/ner"
)

type requestPreparation struct {
	filesChanged bool
	fileResult   fileOutcome
	nerMatches   ner.MatchSet
}

func parallelRequestPreparation(ctx context.Context, provider string, payload any, promptTexts []string, analyzer ner.Analyzer, fileAnonymizer FileAnonymizer) (requestPreparation, error) {
	type nerResult struct {
		matches ner.MatchSet
		err     error
	}
	type fileResult struct {
		changed bool
		outcome fileOutcome
		err     error
	}

	nerResults := make(chan nerResult, 1)
	fileResults := make(chan fileResult, 1)

	go func() {
		matches, err := ner.AnalyzeStrings(ctx, analyzer, promptTexts)
		nerResults <- nerResult{matches: matches, err: err}
	}()
	go func() {
		changed, outcome, err := anonymizeInlineFiles(ctx, provider, fileAnonymizer, payload)
		fileResults <- fileResult{changed: changed, outcome: outcome, err: err}
	}()

	nerOutcome := <-nerResults
	fileOutcome := <-fileResults
	if nerOutcome.err != nil {
		return requestPreparation{}, nerOutcome.err
	}
	if fileOutcome.err != nil {
		return requestPreparation{}, fileOutcome.err
	}

	return requestPreparation{
		filesChanged: fileOutcome.changed,
		fileResult:   fileOutcome.outcome,
		nerMatches:   nerOutcome.matches,
	}, nil
}
