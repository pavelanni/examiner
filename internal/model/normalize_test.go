package model

import "testing"

func TestNormalizeQuestionText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "whitespace only",
			input:    "   \t\n  ",
			expected: "",
		},
		{
			name:     "case insensitivity",
			input:    "What Is Docker?",
			expected: "what is docker",
		},
		{
			name:     "trailing punctuation stripped",
			input:    "Что такое ПИД-регулятор?!..",
			expected: "что такое пид-регулятор",
		},
		{
			name:     "leading, trailing and inner whitespace collapsed",
			input:    "   что   такое \t ПИД-регулятор?   ",
			expected: "что такое пид-регулятор",
		},
		{
			name:     "yo to ye replacement",
			input:    "Ёжик и мёд?",
			expected: "ежик и мед",
		},
		{
			name:     "internal punctuation preserved",
			input:    "REST-архитектура: основные принципы?",
			expected: "rest-архитектура: основные принципы",
		},
		{
			name:     "identical after normalization",
			input:    " Что такое  ПИД-регулятор? ",
			expected: "что такое пид-регулятор",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeQuestionText(tt.input)
			if got != tt.expected {
				t.Errorf("NormalizeQuestionText(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
