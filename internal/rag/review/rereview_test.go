package review

import (
	"strings"
	"testing"
)

func TestCombineReReviewContext(t *testing.T) {
	s := &Service{}

	tests := []struct {
		name            string
		standardCtx     string
		feedbackCtx     string
		expectedParts   []string
		unexpectedParts []string
	}{
		{
			name:            "both contexts present",
			standardCtx:     "Standard context",
			feedbackCtx:     "Feedback context",
			expectedParts:   []string{"Feedback context", "---", "Standard context"},
			unexpectedParts: []string{},
		},
		{
			name:            "only standard context",
			standardCtx:     "Standard context",
			feedbackCtx:     "",
			expectedParts:   []string{"Standard context"},
			unexpectedParts: []string{"---"},
		},
		{
			name:            "only feedback context",
			standardCtx:     "",
			feedbackCtx:     "Feedback context",
			expectedParts:   []string{"Feedback context", "---"},
			unexpectedParts: []string{},
		},
		{
			name:            "both empty",
			standardCtx:     "",
			feedbackCtx:     "",
			expectedParts:   []string{},
			unexpectedParts: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.combineReReviewContext(tt.standardCtx, tt.feedbackCtx)
			for _, part := range tt.expectedParts {
				if !strings.Contains(got, part) {
					t.Errorf("combineReReviewContext() missing expected part %q in result: %q", part, got)
				}
			}
			for _, part := range tt.unexpectedParts {
				if strings.Contains(got, part) {
					t.Errorf("combineReReviewContext() unexpectedly contains %q in result: %q", part, got)
				}
			}
		})
	}
}
